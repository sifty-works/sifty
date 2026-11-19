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

	"github.com/sifty-works/sifty-server/internal/apperr"
	"github.com/sifty-works/sifty-server/internal/catalog"
	"github.com/sifty-works/sifty-server/internal/config"
	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/fetch"
	"github.com/sifty-works/sifty-server/internal/listing"
	"github.com/sifty-works/sifty-server/internal/runner"
	"github.com/sifty-works/sifty-server/internal/server"
	"github.com/sifty-works/sifty-server/internal/source"
)

type Collector struct {
	store   *config.Store
	catalog *catalog.Catalog
	runner  *runner.Runner
	fetcher fetch.Fetcher
	db      *db.DB
	blocked bool
	now     func() time.Time
	log     *slog.Logger

	OnTick func(now time.Time)

	mu      sync.Mutex
	written int64
}

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

func (c *Collector) Register() {
	c.runner.Register(runner.Kind{Name: "collect", Run: c.run, Resume: false})
}

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

func (c *Collector) Ticker(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if !c.blocked && c.Due(now) {
				c.runner.Enqueue(runner.Spec{Kind: "collect"})
			}
			if c.OnTick != nil {
				c.OnTick(now)
			}
		}
	}
}

func (c *Collector) run(ctx context.Context, j *runner.Job) error {
	defer c.runner.Enqueue(runner.Spec{Kind: "recompute_fit"})
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
						excluded := false
						if excl, exclErr := catalog.Excluded(ctx, c.db, p.Title, p.Company, p.ApplyURL); exclErr != nil {
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

func (c *Collector) StatusFunc() func() server.StatusExtra {
	return c.buildStatusExtra
}

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
	needsAttn := c.attentionSources()
	var attentionCount int
	for _, s := range cfg.Sources {
		if !s.Enabled {
			continue
		}
		key, _ := config.SourceKey(s.URL)
		if needsAttn[key] {
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

type SourceStatus struct {
	Text      string `json:"text"`
	Attention bool   `json:"attention"`
}

type Source struct {
	Key     string        `json:"key"`
	Name    string        `json:"name"`
	URL     string        `json:"url"`
	Enabled bool          `json:"enabled"`
	Status  *SourceStatus `json:"status,omitempty"`
}

type SourcesResp struct {
	Sources  []Source `json:"sources"`
	Auto     bool     `json:"auto"`
	Interval string   `json:"interval"`
}

type addSourceReq struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type toggleSourceReq struct {
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
}

type removeSourceReq struct {
	Key string `json:"key"`
}

type scheduleReq struct {
	Auto     bool   `json:"auto"`
	Interval string `json:"interval"`
}

type collectReq struct {
	Force bool `json:"force"`
}

func (c *Collector) updateSources(update func(*config.Config) error) (SourcesResp, error) {
	if err := c.store.Update(update); err != nil {
		return SourcesResp{}, err
	}
	return c.buildSourcesResp(), nil
}

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
			c.runner.Enqueue(runner.Spec{Kind: "collect"})
		}
		return c.buildStatusExtra(), nil
	})
}

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

type sourceRunRec struct {
	started time.Time
	status  string
	found   int
	newCnt  int
	errText string
}

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

func (c *Collector) attentionSources() map[string]bool {
	rows, err := c.db.Query(`SELECT source FROM (SELECT source, status, ROW_NUMBER() OVER (PARTITION BY source ORDER BY started DESC) rn FROM source_run) WHERE rn<=3 GROUP BY source HAVING COUNT(*)=3 AND SUM(status='ok')=0`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var key string
		if rows.Scan(&key) == nil {
			out[key] = true
		}
	}
	return out
}

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

func (c *Collector) listSource(ctx context.Context, s config.SourceConfig) ([]listing.Posting, error) {
	u, _ := url.Parse(s.URL)
	return source.For(u).List(ctx, c.fetcher, source.SiteConfig{Name: s.Name, URL: s.URL})
}

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
	if excl, exclErr := catalog.Excluded(ctx, c.db, p.Title, p.Company, p.ApplyURL); exclErr != nil {
		c.log.Warn("collect: exclusion check after detail", "error", exclErr)
	} else {
		excluded = excl
	}
	return p, excluded
}

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
