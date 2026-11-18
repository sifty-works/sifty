// Package collect implements the collection background job, the sources
// management API, and the POST /api/collect trigger endpoint.
package collect

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/sifty-works/sifty/internal/apperr"
	"github.com/sifty-works/sifty/internal/catalog"
	"github.com/sifty-works/sifty/internal/config"
	"github.com/sifty-works/sifty/internal/db"
	"github.com/sifty-works/sifty/internal/fetch"
	"github.com/sifty-works/sifty/internal/inbox"
	"github.com/sifty-works/sifty/internal/listing"
	"github.com/sifty-works/sifty/internal/runner"
	"github.com/sifty-works/sifty/internal/server"
	"github.com/sifty-works/sifty/internal/source"
)

// Collector orchestrates collection runs and the sources management API.
type Collector struct {
	store   *config.Store
	catalog *catalog.Catalog
	runner  *runner.Runner
	fetcher fetch.Fetcher
	db      *db.DB
	blocked bool // offline without replay: suppress automatic runs
	now     func() time.Time
	log     *slog.Logger

	mu      sync.Mutex
	written int64 // catalogue writes counter
}

// New returns a Collector. blocked must be true when the application is offline
// without a replay fetcher: automatic collection and force=false do nothing.
// now is used for Due checks; pass nil to use time.Now.
func New(
	store *config.Store,
	cat *catalog.Catalog,
	r *runner.Runner,
	f fetch.Fetcher,
	database *db.DB,
	blocked bool,
	now func() time.Time,
	log *slog.Logger,
) *Collector {
	if now == nil {
		now = time.Now
	}
	return &Collector{
		store:   store,
		catalog: cat,
		runner:  r,
		fetcher: f,
		db:      database,
		blocked: blocked,
		now:     now,
		log:     log,
	}
}

// Register registers the collect kind with the runner. Call before runner.Start.
func (c *Collector) Register() {
	c.runner.Register(runner.Kind{Name: "collect", Run: c.run, Resume: false})
}

// Due reports whether a collection run should be started automatically now.
// Returns false when automatic collection is off or the interval since the
// last run has not elapsed. Active-job deduplication is handled by
// runner.Enqueue, which rejects a second queued or running collect job.
func (c *Collector) Due(now time.Time) bool {
	cfg := c.store.Get()
	if !cfg.Collection.Auto {
		return false
	}
	if last, ok := c.runner.Last("collect"); ok {
		if now.Before(last.Created.Add(time.Duration(cfg.Collection.Interval))) {
			return false
		}
	}
	return true
}

// Ticker enqueues a collect run every five minutes when Due. It blocks
// until ctx is cancelled and must be called in its own goroutine.
func (c *Collector) Ticker(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if !c.blocked && c.Due(now) {
				c.runner.Enqueue(runner.Spec{Kind: "collect"}) //nolint:errcheck
			}
		}
	}
}

// run is called by the runner for each collect job.
func (c *Collector) run(ctx context.Context, j *runner.Job) error {
	cfg := c.store.Get()
	var enabled []config.SourceConfig
	for _, s := range cfg.Sources {
		if s.Enabled {
			enabled = append(enabled, s)
		}
	}

	j.Progress(0, len(enabled), "")
	if len(enabled) == 0 {
		return nil
	}

	// Group sources by the API host they contact so that sources sharing an
	// API host (Greenhouse, Lever, Ashby) are read sequentially within one
	// goroutine, holding one net lane slot for the whole group.
	type grp struct{ sources []config.SourceConfig }
	var groups []grp
	groupIdx := make(map[string]int)
	for _, s := range enabled {
		u, err := url.Parse(s.URL)
		if err != nil {
			continue
		}
		h := source.Host(u)
		if i, ok := groupIdx[h]; ok {
			groups[i].sources = append(groups[i].sources, s)
		} else {
			groupIdx[h] = len(groups)
			groups = append(groups, grp{[]config.SourceConfig{s}})
		}
	}

	seenURLs := make(map[string]bool)
	var mu sync.Mutex
	var doneCount, failedCount int

	var wg sync.WaitGroup
	for _, g := range groups {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := j.Lane(ctx, "net")
			if err != nil {
				return
			}
			defer rel()

			for _, s := range g.sources {
				if ctx.Err() != nil {
					return
				}
				current := s.Name
				if current == "" {
					if u, _ := url.Parse(s.URL); u != nil {
						current = u.Host
					}
				}

				mu.Lock()
				j.Progress(doneCount, len(enabled), current)
				mu.Unlock()

				key, _ := config.SourceKey(s.URL)
				srcStarted := c.now()
				postings, listErr := c.listSource(ctx, s)

				var cntNew, cntUpdated, cntUnchanged, cntRemoved, cntExcluded int
				if listErr == nil {
					for _, p := range postings {
						canon, err := listing.Canonical(p.URL)
						if err != nil {
							continue
						}
						mu.Lock()
						skip := seenURLs[canon]
						if !skip {
							seenURLs[canon] = true
						}
						mu.Unlock()
						if skip {
							continue
						}
						k, knownErr := c.catalog.Known(ctx, p)
						if knownErr != nil {
							c.log.Warn("collect: known failed", "url", p.URL, "error", knownErr)
							continue
						}
						if k.Removed {
							cntRemoved++
							continue
						}
						// Skip Detail but still upsert if the posting matches an exclusion.
						excluded := false
						if excl, exclErr := inbox.Excluded(ctx, c.db, p.Title, p.Company, p.ApplyURL); exclErr != nil {
							c.log.Warn("collect: exclusion check", "error", exclErr)
						} else {
							excluded = excl
						}
						if !excluded {
							needDetail := p.Text == "" && (k.New || (!k.HasText &&
								(k.Requested.IsZero() || c.now().Sub(k.Requested) > 7*24*time.Hour)))
							if needDetail {
								p, excluded = c.addVacancyPage(ctx, p, s, excluded)
							}
						}
						_, outcome, upsertErr := c.catalog.Upsert(ctx, p, key)
						if upsertErr != nil {
							c.log.Warn("collect: upsert failed", "url", p.URL, "error", upsertErr)
							continue
						}
						if excluded {
							cntExcluded++
							continue
						}
						switch outcome {
						case catalog.OutcomeNew:
							cntNew++
						case catalog.OutcomeUpdated, catalog.OutcomeMerged:
							cntUpdated++
						case catalog.OutcomeUnchanged:
							cntUnchanged++
						case catalog.OutcomeTombstoned:
							cntRemoved++
						}
						if outcome != catalog.OutcomeTombstoned {
							c.mu.Lock()
							c.written++
							c.mu.Unlock()
						}
					}
				} else {
					c.log.Debug("collect: source failed", "key", key, "error", listErr)
				}

				runStatus, errText := sourceRunStatus(listErr, len(postings))
				c.writeSourceRun(j.ID, key, srcStarted, c.now(), runStatus, errText,
					len(postings), cntNew, cntUpdated, cntUnchanged, cntExcluded, cntRemoved)

				mu.Lock()
				doneCount++
				if listErr != nil {
					failedCount++
				}
				j.Progress(doneCount, len(enabled), "")
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if failedCount > 0 && failedCount == len(enabled) {
		return runner.Permanent(errors.New("Every source failed"))
	}
	return nil
}

// StatusFunc returns a function suitable for server.Options.Status.
func (c *Collector) StatusFunc() func() server.StatusExtra {
	return c.buildStatusExtra
}

// buildStatusExtra assembles the collection-specific status fields.
func (c *Collector) buildStatusExtra() server.StatusExtra {
	var collecting *server.CollectProgress
	for _, j := range c.runner.Active() {
		if j.Kind == "collect" {
			text := "Collecting…"
			if j.Total > 0 && j.Done < j.Total {
				text = fmt.Sprintf("Collecting… %d of %d sources", j.Done+1, j.Total)
			}
			collecting = &server.CollectProgress{Done: j.Done, Total: j.Total, Text: text}
			break
		}
	}
	var lastRun *server.LastRun
	if j, ok := c.runner.Last("collect"); ok {
		switch j.State {
		case "done", "failed", "interrupted":
			msg := ""
			if j.State == "failed" {
				msg = j.Error
			}
			lastRun = &server.LastRun{
				State:    j.State,
				Finished: j.Updated.UTC().Format(time.RFC3339),
				Message:  msg,
			}
		}
	}
	c.mu.Lock()
	written := c.written
	c.mu.Unlock()

	cfg := c.store.Get()
	var attentionCount int
	for _, s := range cfg.Sources {
		if !s.Enabled {
			continue
		}
		key, _ := config.SourceKey(s.URL)
		if needsAttention(c.querySourceRuns(key)) {
			attentionCount++
		}
	}
	var attention *server.Attention
	if attentionCount > 0 {
		noun := "sources need"
		if attentionCount == 1 {
			noun = "source needs"
		}
		attention = &server.Attention{
			Count: attentionCount,
			Text:  strconv.Itoa(attentionCount) + " " + noun + " attention",
		}
	}

	return server.StatusExtra{
		Collecting: collecting,
		LastRun:    lastRun,
		Catalogue:  written,
		Attention:  attention,
	}
}

// --- Sources API types -------------------------------------------------------

// SourceStatus describes the last run result for one source.
type SourceStatus struct {
	Text      string `json:"text"`
	Attention bool   `json:"attention"`
}

// Source is one entry in the sources list.
type Source struct {
	Key     string        `json:"key"`
	Name    string        `json:"name"`
	URL     string        `json:"url"`
	Enabled bool          `json:"enabled"`
	Status  *SourceStatus `json:"status,omitempty"`
}

// SourcesResp is the response body for all /api/sources endpoints and
// PUT /api/sources/schedule.
type SourcesResp struct {
	Sources  []Source `json:"sources"`
	Auto     bool     `json:"auto"`
	Interval string   `json:"interval"`
}

// addSourceReq is the request body for POST /api/sources.
type addSourceReq struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// toggleSourceReq is the request body for PATCH /api/sources.
type toggleSourceReq struct {
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
}

// removeSourceReq is the request body for DELETE /api/sources.
type removeSourceReq struct {
	Key string `json:"key"`
}

// scheduleReq is the request body for PUT /api/sources/schedule.
type scheduleReq struct {
	Auto     bool   `json:"auto"`
	Interval string `json:"interval"`
}

// collectReq is the request body for POST /api/collect.
type collectReq struct {
	Force bool `json:"force"`
}

// updateSources applies update to the config and returns the sources response.
func (c *Collector) updateSources(update func(*config.Config) error) (SourcesResp, error) {
	if err := c.store.Update(update); err != nil {
		return SourcesResp{}, err
	}
	return c.buildSourcesResp(), nil
}

// RegisterRoutes registers all collect-related routes on mux.
func (c *Collector) RegisterRoutes(mux *http.ServeMux) {
	server.Handle(mux, "GET /api/sources", c.log, func(r *http.Request, _ struct{}) (SourcesResp, error) {
		return c.buildSourcesResp(), nil
	})

	server.Handle(mux, "POST /api/sources", c.log, func(r *http.Request, req addSourceReq) (SourcesResp, error) {
		return c.updateSources(func(cfg *config.Config) error {
			cfg.Sources = append(cfg.Sources, config.SourceConfig{
				Name:    req.Name,
				URL:     req.URL,
				Enabled: true,
			})
			return nil
		})
	})

	server.Handle(mux, "PATCH /api/sources", c.log, func(r *http.Request, req toggleSourceReq) (SourcesResp, error) {
		return c.updateSources(func(cfg *config.Config) error {
			for i := range cfg.Sources {
				key, _ := config.SourceKey(cfg.Sources[i].URL)
				if key == req.Key {
					cfg.Sources[i].Enabled = req.Enabled
					return nil
				}
			}
			return apperr.NotFound{Message: "This source is no longer in the list. Refresh the page."}
		})
	})

	server.Handle(mux, "DELETE /api/sources", c.log, func(r *http.Request, req removeSourceReq) (SourcesResp, error) {
		return c.updateSources(func(cfg *config.Config) error {
			for i, s := range cfg.Sources {
				key, _ := config.SourceKey(s.URL)
				if key == req.Key {
					cfg.Sources = append(cfg.Sources[:i], cfg.Sources[i+1:]...)
					return nil
				}
			}
			return apperr.NotFound{Message: "This source is no longer in the list. Refresh the page."}
		})
	})

	server.Handle(mux, "PUT /api/sources/schedule", c.log, func(r *http.Request, req scheduleReq) (SourcesResp, error) {
		d, err := time.ParseDuration(req.Interval)
		if err != nil {
			return SourcesResp{}, apperr.Invalid{Message: req.Interval}
		}
		return c.updateSources(func(cfg *config.Config) error {
			cfg.Collection.Auto = req.Auto
			cfg.Collection.Interval = config.Duration(d)
			return nil
		})
	})

	server.Handle(mux, "POST /api/collect", c.log, func(r *http.Request, req collectReq) (server.StatusExtra, error) {
		if req.Force || (!c.blocked && c.Due(c.now())) {
			c.runner.Enqueue(runner.Spec{Kind: "collect"}) //nolint:errcheck
		}
		return c.buildStatusExtra(), nil
	})
}

// --- Source run helpers ------------------------------------------------------

// sourceRunStatus returns the run status and user-facing error text for a source.
func sourceRunStatus(listErr error, found int) (string, string) {
	if listErr != nil {
		if errors.Is(listErr, source.ErrNothingRecognised) {
			return "error", "Nothing recognised on the page"
		}
		return "error", fetch.UserText(listErr)
	}
	if found == 0 {
		return "empty", ""
	}
	return "ok", ""
}

// writeSourceRun inserts a source_run row for the given job and source key.
func (c *Collector) writeSourceRun(jobID int64, key string, started, finished time.Time,
	status, errText string, found, newCount, updated, unchanged, excluded, removed int) {
	_, err := c.db.Exec(
		`INSERT OR REPLACE INTO source_run
		 (job_id, source, started, finished, status, found, new, updated, unchanged, excluded, removed, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		jobID, key,
		started.UTC().Format(time.RFC3339), finished.UTC().Format(time.RFC3339),
		status, found, newCount, updated, unchanged, excluded, removed, errText,
	)
	if err != nil {
		c.log.Warn("collect: write source_run", "key", key, "error", err)
	}
}

// sourceRunRec is one row from the source_run table.
type sourceRunRec struct {
	started time.Time
	status  string
	found   int
	newCnt  int
	errText string
}

// querySourceRuns returns runs for the given source key, newest first.
func (c *Collector) querySourceRuns(key string) []sourceRunRec {
	rows, err := c.db.Query(
		`SELECT started, status, found, new, error
		 FROM source_run WHERE source = ? ORDER BY job_id DESC`,
		key,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []sourceRunRec
	for rows.Next() {
		var r sourceRunRec
		var startedStr string
		if err := rows.Scan(&startedStr, &r.status, &r.found, &r.newCnt, &r.errText); err != nil {
			continue
		}
		r.started, _ = time.Parse(time.RFC3339, startedStr)
		out = append(out, r)
	}
	return out
}

// buildSourceStatus returns the status for a source, or nil if it never ran.
func (c *Collector) buildSourceStatus(key string) *SourceStatus {
	history := c.querySourceRuns(key)
	if len(history) == 0 {
		return nil
	}
	last := history[0]

	var lastSuccess *sourceRunRec
	for i := range history {
		if history[i].status == "ok" {
			lastSuccess = &history[i]
			break
		}
	}

	now := c.now()
	thirtyDaysAgo := now.Add(-30 * 24 * time.Hour)
	firstOld := !history[len(history)-1].started.After(thirtyDaysAgo)
	noRecentVacancies := true
	for _, r := range history {
		if !r.started.Before(thirtyDaysAgo) && r.status == "ok" {
			noRecentVacancies = false
			break
		}
	}

	var text string
	switch {
	case last.status == "error":
		text = last.errText
		if lastSuccess != nil {
			text += " · last success " + lastSuccess.started.Local().Format("2 Jan 15:04")
		}
	case firstOld && noRecentVacancies:
		text = "No vacancies in 30 days"
	case lastSuccess != nil:
		text = "Last success " + lastSuccess.started.Local().Format("2 Jan 15:04") +
			" · " + strconv.Itoa(lastSuccess.found) + " seen · " +
			strconv.Itoa(lastSuccess.newCnt) + " new"
	}

	return &SourceStatus{Text: text, Attention: needsAttention(history)}
}

// needsAttention reports whether the source's last three runs (at least three)
// all ended in error or empty, using the history already read for the status.
func needsAttention(history []sourceRunRec) bool {
	if len(history) < 3 {
		return false
	}
	for _, r := range history[:3] {
		if r.status == "ok" {
			return false
		}
	}
	return true
}

// listSource fetches the posting list from one source.
func (c *Collector) listSource(ctx context.Context, s config.SourceConfig) ([]listing.Posting, error) {
	u, _ := url.Parse(s.URL)
	return source.For(u).List(ctx, c.fetcher, source.SiteConfig{Name: s.Name, URL: s.URL})
}

// addVacancyPage fetches the vacancy's own page, fills text and empty fields,
// and re-checks exclusions on the updated fields. It returns the updated
// posting and whether it is excluded after the re-check.
func (c *Collector) addVacancyPage(ctx context.Context, p listing.Posting, s config.SourceConfig, excluded bool) (listing.Posting, bool) {
	det, detErr := source.Detail(ctx, c.fetcher, p, source.SiteConfig{Name: s.Name, URL: p.URL})
	switch {
	case detErr == nil:
		p = det
		p.Requested = true
	case errors.Is(detErr, fetch.ErrBlocked):
		p.TextState = listing.TextBlocked
	case !errors.Is(detErr, fetch.ErrOffline) && !errors.Is(detErr, fetch.ErrRefused):
		p.Requested = true
	}
	if excl, exclErr := inbox.Excluded(ctx, c.db, p.Title, p.Company, p.ApplyURL); exclErr != nil {
		c.log.Warn("collect: exclusion check after detail", "error", exclErr)
	} else {
		excluded = excl
	}
	return p, excluded
}

// --- Helpers -----------------------------------------------------------------

// buildSourcesResp assembles the response for all /api/sources endpoints.
func (c *Collector) buildSourcesResp() SourcesResp {
	cfg := c.store.Get()
	sources := make([]Source, len(cfg.Sources))
	for i, s := range cfg.Sources {
		name := s.Name
		if name == "" {
			if u, err := url.Parse(s.URL); err == nil {
				name = u.Host
			}
		}
		key, _ := config.SourceKey(s.URL)
		sources[i] = Source{
			Key:     key,
			Name:    name,
			URL:     s.URL,
			Enabled: s.Enabled,
			Status:  c.buildSourceStatus(key),
		}
	}
	return SourcesResp{
		Sources:  sources,
		Auto:     cfg.Collection.Auto,
		Interval: cfg.Collection.Interval.String(),
	}
}
