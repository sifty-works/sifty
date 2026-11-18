// Package collect implements the collection background job, the sources
// management API, and the POST /api/collect trigger endpoint.
package collect

import (
	"context"
	"encoding/json"
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
				u, _ := url.Parse(s.URL)
				current := s.Name
				if current == "" {
					current = u.Host
				}

				mu.Lock()
				j.Progress(doneCount, len(enabled), current)
				mu.Unlock()

				key, _ := config.SourceKey(s.URL)
				srcStarted := c.now()
				postings, listErr := source.For(u).List(ctx, c.fetcher, source.SiteConfig{Name: s.Name, URL: s.URL})

				var cntNew, cntUpdated, cntUnchanged, cntRemoved int
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
						needDetail := p.Text == "" && (k.New || (!k.HasText &&
							(k.Requested.IsZero() || c.now().Sub(k.Requested) > 7*24*time.Hour)))
						if needDetail {
							cfg := source.SiteConfig{Name: s.Name, URL: p.URL}
							det, detErr := source.Detail(ctx, c.fetcher, p, cfg)
							switch {
							case detErr == nil:
								p = det
								p.Requested = true
							case errors.Is(detErr, fetch.ErrBlocked):
								p.TextState = listing.TextBlocked
							case !errors.Is(detErr, fetch.ErrOffline) && !errors.Is(detErr, fetch.ErrRefused):
								p.Requested = true
							}
						}
						_, outcome, upsertErr := c.catalog.Upsert(ctx, p, key)
						if upsertErr != nil {
							c.log.Warn("collect: upsert failed", "url", p.URL, "error", upsertErr)
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
					len(postings), cntNew, cntUpdated, cntUnchanged, 0, cntRemoved)

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
// It provides "collecting", "lastRun" and "catalogue" keys.
func (c *Collector) StatusFunc() func() map[string]any {
	return func() map[string]any {
		var collecting any
		for _, j := range c.runner.Active() {
			if j.Kind == "collect" {
				text := "Collecting…"
				if j.Total > 0 && j.Done < j.Total {
					text = fmt.Sprintf("Collecting… %d of %d sources", j.Done+1, j.Total)
				}
				collecting = map[string]any{
					"done":  j.Done,
					"total": j.Total,
					"text":  text,
				}
				break
			}
		}
		var lastRun any
		if j, ok := c.runner.Last("collect"); ok {
			switch j.State {
			case "done", "failed", "interrupted":
				msg := ""
				if j.State == "failed" {
					msg = j.Error
				}
				lastRun = map[string]any{
					"state":    j.State,
					"finished": j.Updated.UTC().Format(time.RFC3339),
					"message":  msg,
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
			if c.sourceNeedsAttention(key) {
				attentionCount++
			}
		}
		var attentionVal any
		if attentionCount > 0 {
			noun := "sources need"
			if attentionCount == 1 {
				noun = "source needs"
			}
			attentionVal = map[string]any{
				"count": attentionCount,
				"text":  strconv.Itoa(attentionCount) + " " + noun + " attention",
			}
		}

		return map[string]any{
			"collecting": collecting,
			"lastRun":    lastRun,
			"catalogue":  written,
			"attention":  attentionVal,
		}
	}
}

// RegisterRoutes registers all collect-related routes on mux.
func (c *Collector) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sources", c.handleSourcesList)
	mux.HandleFunc("POST /api/sources", c.handleSourcesAdd)
	mux.HandleFunc("PATCH /api/sources", c.handleSourcesToggle)
	mux.HandleFunc("DELETE /api/sources", c.handleSourcesRemove)
	mux.HandleFunc("PUT /api/sources/schedule", c.handleSchedule)
	mux.HandleFunc("POST /api/collect", c.handleCollect)
}

// --- Sources API -------------------------------------------------------------

func (c *Collector) handleSourcesList(w http.ResponseWriter, _ *http.Request) {
	server.WriteJSON(w, c.sourcesBody())
}

func (c *Collector) handleSourcesAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.MapError(w, apperr.Invalid{Message: "Invalid request body."}, c.log)
		return
	}
	err := c.store.Update(func(cfg *config.Config) error {
		cfg.Sources = append(cfg.Sources, config.SourceConfig{
			Name:    req.Name,
			URL:     req.URL,
			Enabled: true,
		})
		return nil
	})
	if server.MapError(w, err, c.log) {
		return
	}
	server.WriteJSON(w, c.sourcesBody())
}

func (c *Collector) handleSourcesToggle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key     string `json:"key"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.MapError(w, apperr.Invalid{Message: "Invalid request body."}, c.log)
		return
	}
	err := c.store.Update(func(cfg *config.Config) error {
		for i := range cfg.Sources {
			key, _ := config.SourceKey(cfg.Sources[i].URL)
			if key == req.Key {
				cfg.Sources[i].Enabled = req.Enabled
				return nil
			}
		}
		return apperr.NotFound{Message: "This source is no longer in the list. Refresh the page."}
	})
	if server.MapError(w, err, c.log) {
		return
	}
	server.WriteJSON(w, c.sourcesBody())
}

func (c *Collector) handleSourcesRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.MapError(w, apperr.Invalid{Message: "Invalid request body."}, c.log)
		return
	}
	err := c.store.Update(func(cfg *config.Config) error {
		for i, s := range cfg.Sources {
			key, _ := config.SourceKey(s.URL)
			if key == req.Key {
				cfg.Sources = append(cfg.Sources[:i], cfg.Sources[i+1:]...)
				return nil
			}
		}
		return apperr.NotFound{Message: "This source is no longer in the list. Refresh the page."}
	})
	if server.MapError(w, err, c.log) {
		return
	}
	server.WriteJSON(w, c.sourcesBody())
}

func (c *Collector) handleSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Auto     bool   `json:"auto"`
		Interval string `json:"interval"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.MapError(w, apperr.Invalid{Message: "Invalid request body."}, c.log)
		return
	}
	d, err := time.ParseDuration(req.Interval)
	if err != nil {
		server.MapError(w, apperr.Invalid{Message: req.Interval}, c.log)
		return
	}
	updateErr := c.store.Update(func(cfg *config.Config) error {
		cfg.Collection.Auto = req.Auto
		cfg.Collection.Interval = config.Duration(d)
		return nil
	})
	if server.MapError(w, updateErr, c.log) {
		return
	}
	server.WriteJSON(w, c.sourcesBody())
}

// --- POST /api/collect -------------------------------------------------------

func (c *Collector) handleCollect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Force bool `json:"force"`
	}
	json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck // default force=false on parse error

	if req.Force || (!c.blocked && c.Due(c.now())) {
		c.runner.Enqueue(runner.Spec{Kind: "collect"}) //nolint:errcheck
	}

	http.Redirect(w, r, "/api/status", http.StatusSeeOther)
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

// buildSourceStatus returns the status object for a source, or nil if it never ran.
func (c *Collector) buildSourceStatus(key string) map[string]any {
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

	return map[string]any{"text": text, "attention": c.sourceNeedsAttention(key)}
}

// sourceNeedsAttention reports whether the source's last three runs (at least
// three) all ended in error or empty.
func (c *Collector) sourceNeedsAttention(key string) bool {
	rows, err := c.db.Query(
		`SELECT status FROM source_run WHERE source = ? ORDER BY job_id DESC LIMIT 3`,
		key,
	)
	if err != nil {
		return false
	}
	defer rows.Close()
	var statuses []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			continue
		}
		statuses = append(statuses, s)
	}
	if len(statuses) < 3 {
		return false
	}
	for _, s := range statuses {
		if s == "ok" {
			return false
		}
	}
	return true
}

// --- Helpers -----------------------------------------------------------------

// sourcesBody builds the response body for all /api/sources endpoints.
func (c *Collector) sourcesBody() map[string]any {
	cfg := c.store.Get()
	sources := make([]map[string]any, len(cfg.Sources))
	for i, s := range cfg.Sources {
		name := s.Name
		if name == "" {
			if u, err := url.Parse(s.URL); err == nil {
				name = u.Host
			}
		}
		key, _ := config.SourceKey(s.URL)
		sources[i] = map[string]any{
			"key":     key,
			"name":    name,
			"url":     s.URL,
			"enabled": s.Enabled,
			"status":  c.buildSourceStatus(key),
		}
	}
	return map[string]any{
		"sources":  sources,
		"auto":     cfg.Collection.Auto,
		"interval": cfg.Collection.Interval.String(),
	}
}
