package models

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sifty-works/sifty-server/internal/config"
	"github.com/sifty-works/sifty-server/internal/db"
)

type Gate struct {
	db      *db.DB
	store   *config.Store
	offline bool
	secrets map[string]string

	mu      sync.Mutex
	entries map[string]*providerEntry
}

type providerEntry struct {
	model    Model
	mu       chan struct{}
	waitUser int32

	stateMu   sync.RWMutex
	state     string
	loading   bool
	loadStart time.Time
	loadModel string
	loaded    bool
}

func newEntry(m Model, modelName string) *providerEntry {
	ch := make(chan struct{}, 1)
	ch <- struct{}{}
	return &providerEntry{model: m, mu: ch, loadModel: modelName}
}

func NewGate(database *db.DB, store *config.Store, offline bool, secrets map[string]string) *Gate {
	if secrets == nil {
		secrets = map[string]string{}
	}
	return &Gate{db: database, store: store, offline: offline, secrets: secrets, entries: map[string]*providerEntry{}}
}

func (g *Gate) For(kind, class string, vacancy ...int64) Model {
	c := &gatedCall{gate: g, kind: kind, background: class == "background"}
	if len(vacancy) > 0 {
		c.vacancyID = sql.NullInt64{Int64: vacancy[0], Valid: true}
	}
	return c
}

type gatedCall struct {
	gate       *Gate
	kind       string
	background bool
	vacancyID  sql.NullInt64
}

func (c *gatedCall) Complete(ctx context.Context, r Request) (Response, error) {
	if c.gate.offline {
		return Response{}, ErrOffline
	}
	cfg := c.gate.store.Get()
	name := cfg.WorkProviderFor(c.kind)
	if name == "" {
		return Response{}, fmt.Errorf("no provider configured for %s", c.kind)
	}
	pc, ok := cfg.Providers[name]
	if !ok {
		return Response{}, fmt.Errorf("provider %q not found", name)
	}

	if c.background && pc.Cost == "paid" && !cfg.AllowPaidBulk {
		return Response{}, ErrBulkOnPaid
	}

	entry, err := c.gate.getEntry(name, pc)
	if err != nil {
		return Response{}, err
	}

	if c.background {
		paused, _ := c.gate.IsPaused(ctx)
		entry.stateMu.RLock()
		ok := entry.state == "ok" || entry.state == ""
		entry.stateMu.RUnlock()
		if paused || !ok {
			return Response{}, WaitError{RetryAfter: time.Now().Add(5 * time.Minute)}
		}
		if cfg.DailyModelLimit > 0 {
			n, _ := countTodayCalls(ctx, c.gate.db)
			if n >= cfg.DailyModelLimit {
				next := time.Now().UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
				return Response{}, WaitError{RetryAfter: next}
			}
		}
		if atomic.LoadInt32(&entry.waitUser) > 0 {
			return Response{}, WaitError{RetryAfter: time.Now().Add(5 * time.Second)}
		}
		select {
		case <-entry.mu:
		default:
			return Response{}, WaitError{RetryAfter: time.Now().Add(5 * time.Second)}
		}
	} else {
		atomic.AddInt32(&entry.waitUser, 1)
		select {
		case <-entry.mu:
			atomic.AddInt32(&entry.waitUser, -1)
		case <-ctx.Done():
			atomic.AddInt32(&entry.waitUser, -1)
			return Response{}, ctx.Err()
		}
	}
	defer func() { entry.mu <- struct{}{} }()

	start := time.Now()
	entry, err = c.gate.loadIfNeeded(ctx, entry, pc)
	if err != nil {
		c.gate.updateState(entry, err)
		c.gate.journal(ctx, c.kind, name, pc.Model, c.background, len(r.Prompt), 0, 0, 0, int(time.Since(start).Milliseconds()), err.Error(), c.vacancyID)
		return Response{}, err
	}

	resp, callErr := entry.model.Complete(ctx, r)
	dur := time.Since(start)

	outcome := "ok"
	if callErr != nil {
		outcome = callErr.Error()
	}
	c.gate.updateState(entry, callErr)
	c.gate.journal(ctx, c.kind, name, pc.Model, c.background,
		len(r.Prompt), resp.InTokens, len(resp.Text), resp.OutTokens,
		int(dur.Milliseconds()), outcome, c.vacancyID)
	return resp, callErr
}

func (g *Gate) loadIfNeeded(ctx context.Context, e *providerEntry, pc config.ProviderConfig) (*providerEntry, error) {
	loader, ok := e.model.(interface{ Load(context.Context) error })
	if !ok || e.loaded {
		return e, nil
	}
	e.stateMu.Lock()
	e.loading = true
	e.loadStart = time.Now()
	e.stateMu.Unlock()
	err := loader.Load(ctx)
	e.stateMu.Lock()
	e.loading = false
	if err == nil {
		e.loaded = true
	}
	e.stateMu.Unlock()
	return e, err
}

func (g *Gate) updateState(e *providerEntry, err error) {
	e.stateMu.Lock()
	if err == nil {
		e.state = "ok"
	} else {
		e.state = err.Error()
	}
	e.stateMu.Unlock()
}

func (g *Gate) Check(ctx context.Context, name string) (string, error) {
	if g.offline {
		return "Offline", nil
	}
	cfg := g.store.Get()
	pc, ok := cfg.Providers[name]
	if !ok {
		return "", fmt.Errorf("provider %q not found", name)
	}
	entry, err := g.getEntry(name, pc)
	if err != nil {
		return err.Error(), err
	}
	atomic.AddInt32(&entry.waitUser, 1)
	select {
	case <-entry.mu:
		atomic.AddInt32(&entry.waitUser, -1)
	case <-ctx.Done():
		atomic.AddInt32(&entry.waitUser, -1)
		return "", ctx.Err()
	}
	defer func() { entry.mu <- struct{}{} }()
	start := time.Now()
	entry, err = g.loadIfNeeded(ctx, entry, pc)
	if err != nil {
		g.updateState(entry, err)
		g.journal(ctx, "check", name, pc.Model, false, 0, 0, 0, 0, int(time.Since(start).Milliseconds()), err.Error(), sql.NullInt64{})
		return err.Error(), err
	}
	const checkPrompt = "Reply with the single word: OK"
	resp, callErr := entry.model.Complete(ctx, Request{Prompt: checkPrompt})
	dur := time.Since(start)
	g.updateState(entry, callErr)
	outcome := "ok"
	if callErr != nil {
		outcome = callErr.Error()
	}
	g.journal(ctx, "check", name, pc.Model, false, len(checkPrompt), resp.InTokens, len(resp.Text), resp.OutTokens, int(dur.Milliseconds()), outcome, sql.NullInt64{})
	if callErr != nil {
		return callErr.Error(), callErr
	}
	return "ok", nil
}

func (g *Gate) getEntry(name string, pc config.ProviderConfig) (*providerEntry, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if e, ok := g.entries[name]; ok {
		return e, nil
	}
	ctor, ok := adapters[pc.Type]
	if !ok {
		return nil, fmt.Errorf("unknown provider type %q", pc.Type)
	}
	m, err := ctor(pc.Endpoint, pc.Model, pc.KeyEnv, int(time.Duration(pc.Timeout).Milliseconds()), pc.Context, g.secrets)
	if err != nil {
		return nil, err
	}
	e := newEntry(m, pc.Model)
	g.entries[name] = e
	return e, nil
}

func (g *Gate) Status() *ModelsStatus {
	cfg := g.store.Get()
	providers := make(map[string]ProviderState, len(cfg.Providers))
	g.mu.Lock()
	for name := range cfg.Providers {
		e, ok := g.entries[name]
		if !ok {
			providers[name] = ProviderState{State: ""}
			continue
		}
		e.stateMu.RLock()
		ps := ProviderState{State: e.state}
		if e.loading {
			ps.Loading = &LoadingInfo{Model: e.loadModel, StartedAt: e.loadStart.UTC().Format(time.RFC3339)}
		}
		e.stateMu.RUnlock()
		providers[name] = ps
	}
	g.mu.Unlock()
	paused, _ := g.IsPaused(context.Background())
	return &ModelsStatus{Providers: providers, Paused: paused}
}

func (g *Gate) Store() *config.Store { return g.store }

func (g *Gate) TodayBackgroundCalls(ctx context.Context) int {
	n, _ := countTodayCalls(ctx, g.db)
	return n
}

func (g *Gate) IsPaused(ctx context.Context) (bool, error) {
	if g.db == nil {
		return false, nil
	}
	var v string
	err := g.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='model_work_paused'`).Scan(&v)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return v == "true", err
}

func (g *Gate) SetPaused(ctx context.Context, paused bool) error {
	v := "false"
	if paused {
		v = "true"
	}
	_, err := g.db.ExecContext(ctx, `INSERT OR REPLACE INTO settings (key,value) VALUES ('model_work_paused',?)`, v)
	return err
}

func (g *Gate) Stop(ctx context.Context) {
	g.mu.Lock()
	entries := make(map[string]*providerEntry, len(g.entries))
	for k, v := range g.entries {
		entries[k] = v
	}
	g.mu.Unlock()
	for _, e := range entries {
		if u, ok := e.model.(interface{ Unload(context.Context) }); ok {
			u.Unload(ctx)
		}
	}
}

func countTodayCalls(ctx context.Context, database *db.DB) (int, error) {
	if database == nil {
		return 0, nil
	}
	today := time.Now().UTC().Format("2006-01-02")
	var n int
	err := database.QueryRowContext(ctx, `SELECT count(*) FROM model_call WHERE background = 1 AND created_at >= ?`, today+"T00:00:00Z").Scan(&n)
	return n, err
}

func (g *Gate) journal(ctx context.Context, kind, provider, model string, background bool, charsIn, tokensIn, charsOut, tokensOut, durMS int, outcome string, vacancyID sql.NullInt64) {
	if g.db == nil {
		return
	}
	bg := 0
	if background {
		bg = 1
	}
	g.db.ExecContext(ctx, `INSERT INTO model_call (created_at,kind,background,provider,model,chars_in,tokens_in,chars_out,tokens_out,duration_ms,outcome,vacancy_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		time.Now().UTC().Format(time.RFC3339), kind, bg, provider, model, charsIn, tokensIn, charsOut, tokensOut, durMS, outcome, vacancyID,
	)
}
