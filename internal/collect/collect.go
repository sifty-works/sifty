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
	"sync"
	"time"

	"github.com/sifty-works/sifty/internal/apperr"
	"github.com/sifty-works/sifty/internal/catalog"
	"github.com/sifty-works/sifty/internal/config"
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
				postings, listErr := source.For(u).List(ctx, c.fetcher, source.SiteConfig{Name: s.Name, URL: s.URL})

				mu.Lock()
				doneCount++
				if listErr != nil {
					failedCount++
				}
				j.Progress(doneCount, len(enabled), "")
				mu.Unlock()

				if listErr != nil {
					c.log.Warn("collect: source failed", "key", key, "error", listErr)
					continue
				}

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
					_, outcome, upsertErr := c.catalog.Upsert(ctx, p, key)
					if upsertErr != nil {
						c.log.Warn("collect: upsert failed", "url", p.URL, "error", upsertErr)
						continue
					}
					if outcome != catalog.OutcomeTombstoned {
						c.mu.Lock()
						c.written++
						c.mu.Unlock()
					}
				}
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
		return map[string]any{
			"collecting": collecting,
			"lastRun":    lastRun,
			"catalogue":  written,
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
		}
	}
	return map[string]any{
		"sources":  sources,
		"auto":     cfg.Collection.Auto,
		"interval": cfg.Collection.Interval.String(),
	}
}
