package collect_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sifty-works/sifty/internal/catalog"
	"github.com/sifty-works/sifty/internal/collect"
	"github.com/sifty-works/sifty/internal/config"
	"github.com/sifty-works/sifty/internal/db"
	"github.com/sifty-works/sifty/internal/fetch"
	"github.com/sifty-works/sifty/internal/fetch/fetchtest"
	"github.com/sifty-works/sifty/internal/runner"
	"github.com/sifty-works/sifty/internal/server"
)

var base = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// --- test setup helpers ------------------------------------------------------

func openDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"), db.Migrations)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

type env struct {
	srv   *httptest.Server
	fm    *fetchtest.Map
	store *config.Store
	r     *runner.Runner
	col   *collect.Collector
	db    *db.DB
	dir   string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	return newEnvClock(t, func() time.Time { return base })
}

// newEnvClock creates a test environment with a custom clock.
func newEnvClock(t *testing.T, now func() time.Time) *env {
	t.Helper()
	database := openDB(t)
	dir := t.TempDir()
	store, err := config.Open(filepath.Join(dir, "sifty.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	fm := fetchtest.NewMap()
	r := runner.New(database, map[string]int{"net": 4, "cpu": 1, "browser": 1}, now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cat := catalog.New(database, now)
	col := collect.New(store, cat, r, fm, database, false, now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	col.Register()

	h := server.New(server.Options{
		Version: "test", Port: 19999,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Status: col.StatusFunc(),
	}, func(mux *http.ServeMux) {
		col.RegisterRoutes(mux)
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go r.Start(ctx) //nolint:errcheck

	return &env{srv: srv, fm: fm, store: store, r: r, col: col, db: database, dir: dir}
}

func (e *env) get(t *testing.T, path string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	req.Host = "127.0.0.1:19999"
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: status %d", path, resp.StatusCode)
	}
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out) //nolint:errcheck
	return out
}

// mutate sends a JSON request with the given method and returns body + status.
func (e *env) mutate(t *testing.T, method, path string, body map[string]any) (map[string]any, int) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, e.srv.URL+path, bytes.NewReader(b))
	req.Host = "127.0.0.1:19999"
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out) //nolint:errcheck
	return out, resp.StatusCode
}

// waitJob subscribes first, then checks Last to avoid missing a job that
// already finished before the subscription channel was created.
func waitJob(t *testing.T, r *runner.Runner) runner.Job {
	t.Helper()
	ch, unsub := r.Subscribe()
	defer unsub()
	if j, ok := r.Last("collect"); ok && (j.State == "done" || j.State == "failed") {
		return j
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Job.Kind == "collect" && (ev.Job.State == "done" || ev.Job.State == "failed") {
				return ev.Job
			}
		case <-deadline:
			t.Fatal("timed out waiting for collect job to finish")
			return runner.Job{}
		}
	}
}

// --- test fixtures -----------------------------------------------------------

const ghBoardResponse = `{"company":{"name":"Acme Corp"},
"jobs":[{"id":1,"title":"Engineer","absolute_url":"https://boards.greenhouse.io/acme/jobs/1","content":"<p>Job text</p>"},
{"id":2,"title":"Designer","absolute_url":"https://boards.greenhouse.io/acme/jobs/2","content":"<p>Design text</p>"}]}`

const feedXML = `<?xml version="1.0"?>
<rss version="2.0"><channel>
<item><title>Go Developer</title><link>https://feeds.example.com/jobs/3</link><description><![CDATA[<p>Feed job</p>]]></description></item>
</channel></rss>`

const emptyFeedXML = `<?xml version="1.0"?>
<rss version="2.0"><channel></channel></rss>`

const badPageHTML = `<html><body><p>Not a job site</p></body></html>`

// --- Run behaviour -----------------------------------------------------------

func TestRunWithMixedSourcesEndsDone(t *testing.T) {
	e := newEnv(t)
	e.fm.Set("https://boards-api.greenhouse.io/v1/boards/acme/jobs?content=true",
		fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "application/json", Body: []byte(ghBoardResponse)}})
	e.fm.Set("https://feeds.example.com/jobs.xml",
		fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "application/xml", Body: []byte(feedXML)}})
	e.fm.Set("https://failing.example.com/jobs",
		fetchtest.MapEntry{Err: fetch.ErrNotFound})

	e.store.Update(func(c *config.Config) error { //nolint:errcheck
		c.Sources = []config.SourceConfig{
			{URL: "https://boards.greenhouse.io/acme", Enabled: true},
			{URL: "https://feeds.example.com/jobs.xml", Enabled: true},
			{URL: "https://failing.example.com/jobs", Enabled: true},
		}
		return nil
	})

	e.r.Enqueue(runner.Spec{Kind: "collect"}) //nolint:errcheck
	j := waitJob(t, e.r)

	if j.State != "done" {
		t.Errorf("state: got %q, want done", j.State)
	}
	st := e.get(t, "/api/status")
	lr := st["lastRun"].(map[string]any)
	if lr["state"] != "done" {
		t.Errorf("lastRun.state: got %v", lr["state"])
	}
	if lr["message"] != "" {
		t.Errorf("lastRun.message: got %v", lr["message"])
	}
}

func TestRunAllFailingEndsFailed(t *testing.T) {
	e := newEnv(t)
	e.fm.Set("https://failing.example.com/jobs", fetchtest.MapEntry{Err: fetch.ErrNotFound})
	e.fm.Set("https://feeds.example.com/jobs.xml", fetchtest.MapEntry{Err: fetch.ErrTransient})

	e.store.Update(func(c *config.Config) error { //nolint:errcheck
		c.Sources = []config.SourceConfig{
			{URL: "https://failing.example.com/jobs", Enabled: true},
			{URL: "https://feeds.example.com/jobs.xml", Enabled: true},
		}
		return nil
	})

	e.r.Enqueue(runner.Spec{Kind: "collect"}) //nolint:errcheck
	j := waitJob(t, e.r)

	if j.State != "failed" {
		t.Errorf("state: got %q, want failed", j.State)
	}
	st := e.get(t, "/api/status")
	lr := st["lastRun"].(map[string]any)
	if lr["message"] != "Every source failed" {
		t.Errorf("lastRun.message: got %v", lr["message"])
	}
}

func TestRunDeduplicatesPostingsWithinOneRun(t *testing.T) {
	sameURL := "https://feeds.example.com/jobs/shared"
	xmlA := fmt.Sprintf(`<rss version="2.0"><channel><item><title>Shared Job</title><link>%s</link><description>text</description></item></channel></rss>`, sameURL)
	xmlB := fmt.Sprintf(`<rss version="2.0"><channel><item><title>Shared Job</title><link>%s</link><description>other</description></item></channel></rss>`, sameURL)
	e := newEnv(t)
	e.fm.Set("https://feeds.example.com/a.xml", fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "application/xml", Body: []byte(xmlA)}})
	e.fm.Set("https://feeds2.example.com/b.xml", fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "application/xml", Body: []byte(xmlB)}})

	e.store.Update(func(c *config.Config) error { //nolint:errcheck
		c.Sources = []config.SourceConfig{
			{URL: "https://feeds.example.com/a.xml", Enabled: true},
			{URL: "https://feeds2.example.com/b.xml", Enabled: true},
		}
		return nil
	})

	e.r.Enqueue(runner.Spec{Kind: "collect"}) //nolint:errcheck
	waitJob(t, e.r)

	// Catalogue counter must be 1 (one write, not two for the same URL).
	if cat := e.get(t, "/api/status")["catalogue"].(float64); cat != 1 {
		t.Errorf("catalogue: got %v, want 1", cat)
	}
	// Exactly one request per source.
	if reqs := e.fm.Requests(); len(reqs) != 2 {
		t.Errorf("requests: got %d, want 2: %v", len(reqs), reqs)
	}
}

func TestRunTwoBoardsOnSameAPIHostReadSequentially(t *testing.T) {
	e := newEnv(t)
	e.fm.Set("https://boards-api.greenhouse.io/v1/boards/acme/jobs?content=true",
		fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "application/json", Body: []byte(ghBoardResponse)}})
	e.fm.Set("https://boards-api.greenhouse.io/v1/boards/otherco/jobs?content=true",
		fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "application/json",
			Body: []byte(`{"company":{"name":"OtherCo"},"jobs":[{"id":10,"title":"Dev","absolute_url":"https://boards.greenhouse.io/otherco/jobs/10","content":"<p>x</p>"}]}`)}})

	e.store.Update(func(c *config.Config) error { //nolint:errcheck
		c.Sources = []config.SourceConfig{
			{URL: "https://boards.greenhouse.io/acme", Enabled: true},
			{URL: "https://boards.greenhouse.io/otherco", Enabled: true},
		}
		return nil
	})

	e.r.Enqueue(runner.Spec{Kind: "collect"}) //nolint:errcheck
	j := waitJob(t, e.r)
	if j.State != "done" {
		t.Errorf("state: got %q, want done", j.State)
	}
	// Both boards must be fetched; each board is one request.
	if reqs := e.fm.Requests(); len(reqs) != 2 {
		t.Errorf("requests: got %d, want 2 (one per board): %v", len(reqs), reqs)
	}
}

// --- Due and POST /api/collect -----------------------------------------------

func TestDueAutoOnOffAndWhileQueued(t *testing.T) {
	// Auto on, no prior run → Due returns true.
	e := newEnv(t)
	if !e.col.Due(base) {
		t.Error("Due: want true when no prior run and auto is on")
	}
	// Auto off → Due returns false.
	e.store.Update(func(c *config.Config) error { c.Collection.Auto = false; return nil }) //nolint:errcheck
	if e.col.Due(base) {
		t.Error("Due: want false when auto is off")
	}
}

func TestDueReturnsFalseWhileRunQueued(t *testing.T) {
	e := newEnv(t)
	e.fm.Set("https://feeds.example.com/jobs.xml", fetchtest.MapEntry{Err: fetch.ErrNotFound})
	e.store.Update(func(c *config.Config) error { //nolint:errcheck
		c.Sources = []config.SourceConfig{{URL: "https://feeds.example.com/jobs.xml", Enabled: true}}
		return nil
	})
	e.r.Enqueue(runner.Spec{Kind: "collect"}) //nolint:errcheck
	if e.col.Due(base) {
		t.Error("Due: want false while collect is queued or running")
	}
	waitJob(t, e.r)
}

func TestDueIntervalMeasuredFromLastRunCreated(t *testing.T) {
	e := newEnv(t)
	e.fm.Set("https://feeds.example.com/jobs.xml", fetchtest.MapEntry{Err: fetch.ErrNotFound})
	e.store.Update(func(c *config.Config) error { //nolint:errcheck
		c.Sources = []config.SourceConfig{{URL: "https://feeds.example.com/jobs.xml", Enabled: true}}
		c.Collection.Interval = config.Duration(2 * time.Hour)
		return nil
	})
	e.r.Enqueue(runner.Spec{Kind: "collect"}) //nolint:errcheck
	waitJob(t, e.r)

	if e.col.Due(base.Add(1 * time.Hour)) {
		t.Error("Due: want false when interval has not elapsed")
	}
	if !e.col.Due(base.Add(2 * time.Hour)) {
		t.Error("Due: want true once interval elapsed")
	}
}

func TestPostCollectForceFalseEnqueuesWhenDue(t *testing.T) {
	e := newEnv(t)
	e.fm.Set("https://feeds.example.com/jobs.xml", fetchtest.MapEntry{Err: fetch.ErrNotFound})
	e.store.Update(func(c *config.Config) error { //nolint:errcheck
		c.Sources = []config.SourceConfig{{URL: "https://feeds.example.com/jobs.xml", Enabled: true}}
		return nil
	})
	e.mutate(t, "POST", "/api/collect", map[string]any{"force": false})
	waitJob(t, e.r)
}

func TestPostCollectForceTrueIgnoresAutoSetting(t *testing.T) {
	e := newEnv(t)
	e.fm.Set("https://feeds.example.com/jobs.xml", fetchtest.MapEntry{Err: fetch.ErrNotFound})
	e.store.Update(func(c *config.Config) error { //nolint:errcheck
		c.Sources = []config.SourceConfig{{URL: "https://feeds.example.com/jobs.xml", Enabled: true}}
		c.Collection.Auto = false
		return nil
	})
	_, status := e.mutate(t, "POST", "/api/collect", map[string]any{"force": true})
	if status != 200 {
		t.Fatalf("status: %d", status)
	}
	j := waitJob(t, e.r)
	if j.State != "failed" {
		t.Errorf("state: got %q (ErrNotFound → all failed)", j.State)
	}
	// Status response must carry all expected keys.
	st := e.get(t, "/api/status")
	for _, key := range []string{"version", "offline", "debug", "collecting", "lastRun", "catalogue"} {
		if _, ok := st[key]; !ok {
			t.Errorf("status key %q missing from response", key)
		}
	}
}

func TestPostCollectNoSecondRunWhileOneQueued(t *testing.T) {
	e := newEnv(t)
	e.r.Enqueue(runner.Spec{Kind: "collect"}) //nolint:errcheck
	active1 := e.r.Active()
	e.mutate(t, "POST", "/api/collect", map[string]any{"force": true})
	if len(e.r.Active()) > len(active1) {
		t.Error("second run was enqueued while first is queued/running")
	}
}

func TestPostCollectOfflineBlocksAutomatic(t *testing.T) {
	database := openDB(t)
	dir := t.TempDir()
	store, _ := config.Open(filepath.Join(dir, "sifty.yaml"))
	fm := fetchtest.NewMap()
	now := func() time.Time { return base }
	r := runner.New(database, map[string]int{"net": 4}, now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cat := catalog.New(database, now)
	col := collect.New(store, cat, r, fm, database, true, now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	col.Register()
	h := server.New(server.Options{Version: "test", Port: 19999,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Status: col.StatusFunc(),
	}, func(mux *http.ServeMux) { col.RegisterRoutes(mux) })
	srv := httptest.NewServer(h)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Start(ctx) //nolint:errcheck

	do := func(body map[string]any) int {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", srv.URL+"/api/collect", bytes.NewReader(b))
		req.Host = "127.0.0.1:19999"
		req.Header.Set("Content-Type", "application/json")
		resp, err := srv.Client().Do(req)
		if err != nil {
			return 0
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	do(map[string]any{"force": false})
	if len(r.Active()) != 0 {
		t.Error("force=false should not enqueue when blocked")
	}
	do(map[string]any{"force": true})
	waitJob(t, r) // job completes instantly (no sources); waitJob fails on timeout
}

// --- /api/sources endpoints --------------------------------------------------

func TestSourcesListIsInitiallyEmpty(t *testing.T) {
	e := newEnv(t)
	body := e.get(t, "/api/sources")
	if sources := body["sources"].([]any); len(sources) != 0 {
		t.Errorf("sources: got %d, want 0", len(sources))
	}
	if body["auto"] != true {
		t.Errorf("auto: got %v", body["auto"])
	}
	if body["interval"] != "3h" {
		t.Errorf("interval: got %v", body["interval"])
	}
}

func TestAddSourceAppearsInList(t *testing.T) {
	e := newEnv(t)
	body, status := e.mutate(t, "POST", "/api/sources", map[string]any{
		"name": "My Feed",
		"url":  "https://feeds.example.com/jobs.xml",
	})
	if status != 200 {
		t.Fatalf("status: %d body: %v", status, body)
	}
	sources := body["sources"].([]any)
	if len(sources) != 1 {
		t.Fatalf("sources: got %d, want 1", len(sources))
	}
	s := sources[0].(map[string]any)
	if s["name"] != "My Feed" || s["url"] != "https://feeds.example.com/jobs.xml" || s["enabled"] != true {
		t.Errorf("source: %v", s)
	}
	data, _ := os.ReadFile(filepath.Join(e.dir, "sifty.yaml"))
	if !strings.Contains(string(data), "feeds.example.com") {
		t.Error("settings file should contain new source URL")
	}
}

func TestAddSourceValidationMessages(t *testing.T) {
	e := newEnv(t)
	e.mutate(t, "POST", "/api/sources", map[string]any{"url": "https://feeds.example.com/jobs.xml"}) //nolint:errcheck

	check := func(body map[string]any, wantMsg string) {
		t.Helper()
		resp, status := e.mutate(t, "POST", "/api/sources", body)
		if status != 422 {
			t.Errorf("status: got %d, want 422; body: %v", status, resp)
		}
		if !strings.Contains(fmt.Sprint(resp["error"]), wantMsg) {
			t.Errorf("error: got %v, want to contain %q", resp["error"], wantMsg)
		}
	}

	check(map[string]any{"url": "not-a-url"}, "Enter an http(s) address.")
	check(map[string]any{"url": "https://feeds.example.com/jobs.xml"}, "This source is already in the list.")
	check(map[string]any{"name": strings.Repeat("x", 101), "url": "https://other.example.com/jobs.xml"}, "The name is at most 100 characters.")
}

func TestToggleAndRemoveSourceWithUnknownKeyReturn404(t *testing.T) {
	e := newEnv(t)
	for _, method := range []string{"PATCH", "DELETE"} {
		_, status := e.mutate(t, method, "/api/sources", map[string]any{"key": "https://no-such.example.com"})
		if status != 404 {
			t.Errorf("%s unknown key: got %d, want 404", method, status)
		}
	}
}

func TestToggleSourceEnabled(t *testing.T) {
	e := newEnv(t)
	body, _ := e.mutate(t, "POST", "/api/sources", map[string]any{"url": "https://feeds.example.com/jobs.xml"})
	key := body["sources"].([]any)[0].(map[string]any)["key"].(string)

	body, status := e.mutate(t, "PATCH", "/api/sources", map[string]any{"key": key, "enabled": false})
	if status != 200 {
		t.Fatalf("status: %d", status)
	}
	if body["sources"].([]any)[0].(map[string]any)["enabled"] != false {
		t.Error("source should be disabled after PATCH")
	}
}

func TestRemoveSourceDeletesFromList(t *testing.T) {
	e := newEnv(t)
	body, _ := e.mutate(t, "POST", "/api/sources", map[string]any{"url": "https://feeds.example.com/jobs.xml"})
	key := body["sources"].([]any)[0].(map[string]any)["key"].(string)

	body, status := e.mutate(t, "DELETE", "/api/sources", map[string]any{"key": key})
	if status != 200 {
		t.Fatalf("status: %d", status)
	}
	if len(body["sources"].([]any)) != 0 {
		t.Error("source should be gone after DELETE")
	}
}

func TestPutScheduleValidAndInvalid(t *testing.T) {
	e := newEnv(t)

	body, status := e.mutate(t, "PUT", "/api/sources/schedule", map[string]any{"auto": false, "interval": "1h"})
	if status != 200 {
		t.Fatalf("valid schedule: status %d body %v", status, body)
	}
	if body["auto"] != false || body["interval"] != "1h" {
		t.Errorf("schedule: got auto=%v interval=%v", body["auto"], body["interval"])
	}

	body, status = e.mutate(t, "PUT", "/api/sources/schedule", map[string]any{"auto": true, "interval": "badval"})
	if status != 422 {
		t.Fatalf("bad interval: status %d", status)
	}
	if !strings.Contains(fmt.Sprint(body["error"]), "badval") {
		t.Errorf("error should contain bad value; got: %v", body["error"])
	}

	body, status = e.mutate(t, "PUT", "/api/sources/schedule", map[string]any{"auto": true, "interval": "10m"})
	if status != 422 {
		t.Fatalf("out-of-range interval: status %d", status)
	}
	if !strings.Contains(fmt.Sprint(body["error"]), "The interval must be between 15 minutes and 7 days.") {
		t.Errorf("error message: %v", body["error"])
	}
}

// --- GET /api/status ---------------------------------------------------------

func TestStatusCatalogueCounterIncreasesAfterRun(t *testing.T) {
	e := newEnv(t)
	e.fm.Set("https://feeds.example.com/jobs.xml",
		fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "application/xml", Body: []byte(feedXML)}})
	e.store.Update(func(c *config.Config) error { //nolint:errcheck
		c.Sources = []config.SourceConfig{{URL: "https://feeds.example.com/jobs.xml", Enabled: true}}
		return nil
	})

	before := e.get(t, "/api/status")["catalogue"].(float64)
	e.r.Enqueue(runner.Spec{Kind: "collect"}) //nolint:errcheck
	waitJob(t, e.r)
	after := e.get(t, "/api/status")["catalogue"].(float64)
	if after <= before {
		t.Errorf("catalogue should increase after run; before=%v after=%v", before, after)
	}
	// Counter is stable between runs (no spurious increments).
	c1 := e.get(t, "/api/status")["catalogue"].(float64)
	if c2 := e.get(t, "/api/status")["catalogue"].(float64); c1 != c2 {
		t.Errorf("catalogue changed without a run: %v → %v", c1, c2)
	}
	// collecting key is nil once the job has finished.
	if e.get(t, "/api/status")["collecting"] != nil {
		t.Error("collecting should be nil after run")
	}
}

// --- source_run rows ---------------------------------------------------------

// sourcesOf extracts the sources slice from a /api/sources response.
func sourcesOf(body map[string]any) []map[string]any {
	raw := body["sources"].([]any)
	out := make([]map[string]any, len(raw))
	for i, r := range raw {
		out[i] = r.(map[string]any)
	}
	return out
}

func TestRunWritesSourceRunRowsForEachOutcome(t *testing.T) {
	e := newEnv(t)

	// ok board (Greenhouse): 2 jobs
	e.fm.Set("https://boards-api.greenhouse.io/v1/boards/acme/jobs?content=true",
		fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "application/json", Body: []byte(ghBoardResponse)}})
	// empty feed
	e.fm.Set("https://feeds.example.com/empty.xml",
		fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "application/xml", Body: []byte(emptyFeedXML)}})
	// blocked source
	e.fm.Set("https://blocked.example.com/jobs",
		fetchtest.MapEntry{Err: fetch.ErrBlocked})
	// unrecognised page – serve HTML with no job listings (generic source returns ErrNothingRecognised)
	e.fm.Set("https://noparse.example.com/jobs",
		fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "text/html", Body: []byte(badPageHTML)}})

	e.store.Update(func(c *config.Config) error { //nolint:errcheck
		c.Sources = []config.SourceConfig{
			{URL: "https://boards.greenhouse.io/acme", Enabled: true},
			{URL: "https://feeds.example.com/empty.xml", Enabled: true},
			{URL: "https://blocked.example.com/jobs", Enabled: true},
			{URL: "https://noparse.example.com/jobs", Enabled: true},
		}
		return nil
	})

	e.r.Enqueue(runner.Spec{Kind: "collect"}) //nolint:errcheck
	waitJob(t, e.r)

	srcs := sourcesOf(e.get(t, "/api/sources"))
	if len(srcs) != 4 {
		t.Fatalf("sources: got %d, want 4", len(srcs))
	}

	for _, s := range srcs {
		st := s["status"]
		switch s["url"] {
		case "https://boards.greenhouse.io/acme":
			// ok run: status object with text containing "Last success"
			if st == nil {
				t.Errorf("ok source: status is nil")
				continue
			}
			text := st.(map[string]any)["text"].(string)
			if !strings.HasPrefix(text, "Last success") {
				t.Errorf("ok source text: got %q, want prefix 'Last success'", text)
			}
			// found=2, new=2 — text ends with "2 seen · 2 new"
			if !strings.HasSuffix(text, "2 seen · 2 new") {
				t.Errorf("ok source text counts: got %q", text)
			}
		case "https://feeds.example.com/empty.xml":
			// empty run: status is not nil but text is empty (no success, no error)
			if st == nil {
				t.Errorf("empty source: status is nil")
			}
		case "https://blocked.example.com/jobs":
			if st == nil {
				t.Errorf("blocked source: status is nil")
				continue
			}
			text := st.(map[string]any)["text"].(string)
			if !strings.Contains(text, "Blocked by site") {
				t.Errorf("blocked source text: got %q, want 'Blocked by site'", text)
			}
		case "https://noparse.example.com/jobs":
			if st == nil {
				t.Errorf("noparse source: status is nil")
				continue
			}
			text := st.(map[string]any)["text"].(string)
			if !strings.Contains(text, "Nothing recognised on the page") {
				t.Errorf("noparse source text: got %q, want 'Nothing recognised on the page'", text)
			}
		}
	}
}

func TestSourceStatusErrorWithAndWithoutLastSuccess(t *testing.T) {
	e := newEnv(t)
	e.fm.Set("https://feeds.example.com/jobs.xml",
		fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "application/xml", Body: []byte(feedXML)}})
	e.store.Update(func(c *config.Config) error { //nolint:errcheck
		c.Sources = []config.SourceConfig{{URL: "https://feeds.example.com/jobs.xml", Enabled: true}}
		return nil
	})

	// First run: success
	e.r.Enqueue(runner.Spec{Kind: "collect"}) //nolint:errcheck
	waitJob(t, e.r)
	srcs := sourcesOf(e.get(t, "/api/sources"))
	text := srcs[0]["status"].(map[string]any)["text"].(string)
	if !strings.HasPrefix(text, "Last success") {
		t.Errorf("after success: text = %q, want prefix 'Last success'", text)
	}

	// Second run: failure (no prior success → error only; but there IS a prior success here)
	e.fm.Set("https://feeds.example.com/jobs.xml", fetchtest.MapEntry{Err: fetch.ErrBlocked})
	e.r.Enqueue(runner.Spec{Kind: "collect"}) //nolint:errcheck
	waitJob(t, e.r)
	srcs = sourcesOf(e.get(t, "/api/sources"))
	text = srcs[0]["status"].(map[string]any)["text"].(string)
	if !strings.Contains(text, "Blocked by site") {
		t.Errorf("after failure with prior success: text = %q, want 'Blocked by site'", text)
	}
	if !strings.Contains(text, "last success") {
		t.Errorf("after failure with prior success: text = %q, want 'last success'", text)
	}
}

func TestSourceStatusErrorWithoutPriorSuccess(t *testing.T) {
	e := newEnv(t)
	e.fm.Set("https://feeds.example.com/jobs.xml", fetchtest.MapEntry{Err: fetch.ErrNotFound})
	e.store.Update(func(c *config.Config) error { //nolint:errcheck
		c.Sources = []config.SourceConfig{{URL: "https://feeds.example.com/jobs.xml", Enabled: true}}
		return nil
	})
	e.r.Enqueue(runner.Spec{Kind: "collect"}) //nolint:errcheck
	waitJob(t, e.r)
	srcs := sourcesOf(e.get(t, "/api/sources"))
	text := srcs[0]["status"].(map[string]any)["text"].(string)
	if text != "Not found" {
		t.Errorf("error without prior success: text = %q, want 'Not found'", text)
	}
}

func TestSourceStatusNoVacanciesIn30Days(t *testing.T) {
	var mu sync.Mutex
	nowTime := base
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return nowTime }

	e := newEnvClock(t, now)
	e.fm.Set("https://feeds.example.com/empty.xml",
		fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "application/xml", Body: []byte(emptyFeedXML)}})
	e.store.Update(func(c *config.Config) error { //nolint:errcheck
		c.Sources = []config.SourceConfig{{URL: "https://feeds.example.com/empty.xml", Enabled: true}}
		return nil
	})

	// First run at base: source is empty
	e.r.Enqueue(runner.Spec{Kind: "collect"}) //nolint:errcheck
	waitJob(t, e.r)

	// Advance clock past 30 days
	mu.Lock()
	nowTime = base.Add(31 * 24 * time.Hour)
	mu.Unlock()

	// Second run: still empty
	e.r.Enqueue(runner.Spec{Kind: "collect"}) //nolint:errcheck
	waitJob(t, e.r)

	srcs := sourcesOf(e.get(t, "/api/sources"))
	text := srcs[0]["status"].(map[string]any)["text"].(string)
	if text != "No vacancies in 30 days" {
		t.Errorf("30-day rule: text = %q, want 'No vacancies in 30 days'", text)
	}
}

func TestAttentionAfterThreeFailingRunsNotAfterTwo(t *testing.T) {
	e := newEnv(t)
	e.fm.Set("https://feeds.example.com/jobs.xml", fetchtest.MapEntry{Err: fetch.ErrBlocked})
	e.store.Update(func(c *config.Config) error { //nolint:errcheck
		c.Sources = []config.SourceConfig{{URL: "https://feeds.example.com/jobs.xml", Enabled: true}}
		return nil
	})

	for run := 1; run <= 3; run++ {
		e.r.Enqueue(runner.Spec{Kind: "collect"}) //nolint:errcheck
		waitJob(t, e.r)
		srcs := sourcesOf(e.get(t, "/api/sources"))
		got := srcs[0]["status"].(map[string]any)["attention"].(bool)
		want := run >= 3
		if got != want {
			t.Errorf("after %d runs: attention = %v, want %v", run, got, want)
		}
	}
}

func TestStatusAttentionCountAndSingular(t *testing.T) {
	e := newEnv(t)
	e.fm.Set("https://feeds.example.com/a.xml", fetchtest.MapEntry{Err: fetch.ErrBlocked})
	e.fm.Set("https://feeds2.example.com/b.xml", fetchtest.MapEntry{Err: fetch.ErrBlocked})
	e.store.Update(func(c *config.Config) error { //nolint:errcheck
		c.Sources = []config.SourceConfig{
			{URL: "https://feeds.example.com/a.xml", Enabled: true},
			{URL: "https://feeds2.example.com/b.xml", Enabled: true},
		}
		return nil
	})

	// Run 3 times to trigger attention on both sources
	for i := 0; i < 3; i++ {
		e.r.Enqueue(runner.Spec{Kind: "collect"}) //nolint:errcheck
		waitJob(t, e.r)
	}

	st := e.get(t, "/api/status")
	att := st["attention"].(map[string]any)
	if att["count"].(float64) != 2 {
		t.Errorf("attention.count: got %v, want 2", att["count"])
	}
	if att["text"] != "2 sources need attention" {
		t.Errorf("attention.text: got %v, want '2 sources need attention'", att["text"])
	}

	// Disable one source; attention count must drop to 1 with singular text
	e.store.Update(func(c *config.Config) error { //nolint:errcheck
		c.Sources[1].Enabled = false
		return nil
	})
	st = e.get(t, "/api/status")
	att = st["attention"].(map[string]any)
	if att["count"].(float64) != 1 {
		t.Errorf("after disable: attention.count: got %v, want 1", att["count"])
	}
	if att["text"] != "1 source needs attention" {
		t.Errorf("after disable: attention.text: got %v", att["text"])
	}
}

func TestSourceRunRowDeletedWithJob(t *testing.T) {
	e := newEnv(t)
	e.fm.Set("https://feeds.example.com/jobs.xml",
		fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "application/xml", Body: []byte(feedXML)}})
	e.store.Update(func(c *config.Config) error { //nolint:errcheck
		c.Sources = []config.SourceConfig{{URL: "https://feeds.example.com/jobs.xml", Enabled: true}}
		return nil
	})

	e.r.Enqueue(runner.Spec{Kind: "collect"}) //nolint:errcheck
	j := waitJob(t, e.r)

	// Confirm source_run row was written
	var cnt int
	e.db.QueryRow(`SELECT COUNT(*) FROM source_run WHERE job_id=?`, j.ID).Scan(&cnt) //nolint:errcheck
	if cnt == 0 {
		t.Fatal("source_run: expected a row but found none")
	}

	// Delete the job; CASCADE should remove the source_run row
	e.db.Exec(`DELETE FROM job WHERE id=?`, j.ID)                                    //nolint:errcheck
	e.db.QueryRow(`SELECT COUNT(*) FROM source_run WHERE job_id=?`, j.ID).Scan(&cnt) //nolint:errcheck
	if cnt != 0 {
		t.Errorf("source_run row should be deleted with its job, got %d rows", cnt)
	}
}
