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
	return newEnvClock(t, func() time.Time { return base }, false)
}

// addSource sets e's config to a single enabled source at url.
func addSource(t *testing.T, e *env, url string) {
	t.Helper()
	e.store.Update(func(c *config.Config) error { //nolint:errcheck
		c.Sources = []config.SourceConfig{{URL: url, Enabled: true}}
		return nil
	})
}

// setSources replaces e's config sources with one enabled entry per url.
func setSources(t *testing.T, e *env, urls ...string) {
	t.Helper()
	srcs := make([]config.SourceConfig, len(urls))
	for i, u := range urls {
		srcs[i] = config.SourceConfig{URL: u, Enabled: true}
	}
	e.store.Update(func(c *config.Config) error { c.Sources = srcs; return nil }) //nolint:errcheck
}

// runCollect enqueues a forced collect run and waits for it to finish.
func runCollect(t *testing.T, e *env) runner.Job {
	t.Helper()
	e.r.Enqueue(runner.Spec{Kind: "collect"}) //nolint:errcheck
	return waitJob(t, e.r)
}

// newEnvClock creates a test environment with a custom clock and optional offline mode.
func newEnvClock(t *testing.T, now func() time.Time, offline bool) *env {
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
	col := collect.New(store, cat, r, fm, database, offline, now, slog.New(slog.NewTextHandler(io.Discard, nil)))
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

// waitJob polls Last until the most recent collect job reaches a final state.
func waitJob(t *testing.T, r *runner.Runner) runner.Job {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if j, ok := r.Last("collect"); ok && (j.State == "done" || j.State == "failed") {
			return j
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for collect job to finish")
			return runner.Job{}
		default:
			time.Sleep(5 * time.Millisecond)
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

	setSources(t, e, "https://boards.greenhouse.io/acme", "https://feeds.example.com/jobs.xml", "https://failing.example.com/jobs")

	j := runCollect(t, e)

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

	setSources(t, e, "https://failing.example.com/jobs", "https://feeds.example.com/jobs.xml")

	j := runCollect(t, e)

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

	setSources(t, e, "https://feeds.example.com/a.xml", "https://feeds2.example.com/b.xml")

	runCollect(t, e)

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

	setSources(t, e, "https://boards.greenhouse.io/acme", "https://boards.greenhouse.io/otherco")

	j := runCollect(t, e)
	if j.State != "done" {
		t.Errorf("state: got %q, want done", j.State)
	}
	// Both boards must be fetched; each board is one request.
	if reqs := e.fm.Requests(); len(reqs) != 2 {
		t.Errorf("requests: got %d, want 2 (one per board): %v", len(reqs), reqs)
	}
}

// --- Due and POST /api/collect -----------------------------------------------

func TestDueAndCollectDeduplication(t *testing.T) {
	e := newEnv(t)
	e.fm.Set("https://feeds.example.com/jobs.xml", fetchtest.MapEntry{Err: fetch.ErrNotFound})
	addSource(t, e, "https://feeds.example.com/jobs.xml")

	// No prior run, auto=true → Due returns true.
	if !e.col.Due(base) {
		t.Error("Due: want true when no prior run and auto is on")
	}

	// POST enqueues a run; Due returns false immediately after (interval not elapsed).
	e.mutate(t, "POST", "/api/collect", map[string]any{"force": false})
	if e.col.Due(base) {
		t.Error("Due: want false after collect is enqueued (interval not yet elapsed)")
	}

	// Two Enqueue calls while a job is queued or running return the same id.
	id1, _ := e.r.Enqueue(runner.Spec{Kind: "collect"})
	id2, _ := e.r.Enqueue(runner.Spec{Kind: "collect"})
	if id1 != id2 {
		t.Errorf("dedup: second Enqueue returned id %d, want same as first (%d)", id2, id1)
	}

	waitJob(t, e.r)

	// Auto off → Due returns false regardless.
	e.store.Update(func(c *config.Config) error { c.Collection.Auto = false; return nil }) //nolint:errcheck
	if e.col.Due(base) {
		t.Error("Due: want false when auto is off")
	}
}

func TestDueIntervalMeasuredFromLastRunCreated(t *testing.T) {
	e := newEnv(t)
	e.fm.Set("https://feeds.example.com/jobs.xml", fetchtest.MapEntry{Err: fetch.ErrNotFound})
	addSource(t, e, "https://feeds.example.com/jobs.xml")
	e.store.Update(func(c *config.Config) error { //nolint:errcheck
		c.Collection.Interval = config.Duration(2 * time.Hour)
		return nil
	})
	runCollect(t, e)

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
	addSource(t, e, "https://feeds.example.com/jobs.xml")
	e.mutate(t, "POST", "/api/collect", map[string]any{"force": false})
	waitJob(t, e.r)
}

func TestPostCollectForceTrueIgnoresAutoSetting(t *testing.T) {
	e := newEnv(t)
	e.fm.Set("https://feeds.example.com/jobs.xml", fetchtest.MapEntry{Err: fetch.ErrNotFound})
	addSource(t, e, "https://feeds.example.com/jobs.xml")
	e.store.Update(func(c *config.Config) error { //nolint:errcheck
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

func TestPostCollectOfflineBlocksAutomatic(t *testing.T) {
	e := newEnvClock(t, func() time.Time { return base }, true)
	e.mutate(t, "POST", "/api/collect", map[string]any{"force": false})
	if len(e.r.Active()) != 0 {
		t.Error("force=false should not enqueue when offline")
	}
	e.mutate(t, "POST", "/api/collect", map[string]any{"force": true})
	waitJob(t, e.r) // job completes instantly (no sources)
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

func TestSourceCRUD(t *testing.T) {
	e := newEnv(t)
	for _, method := range []string{"PATCH", "DELETE"} {
		_, status := e.mutate(t, method, "/api/sources", map[string]any{"key": "https://no-such.example.com"})
		if status != 404 {
			t.Errorf("%s unknown key: got %d, want 404", method, status)
		}
	}
	body, _ := e.mutate(t, "POST", "/api/sources", map[string]any{"url": "https://feeds.example.com/jobs.xml"})
	key := body["sources"].([]any)[0].(map[string]any)["key"].(string)
	body, status := e.mutate(t, "PATCH", "/api/sources", map[string]any{"key": key, "enabled": false})
	if status != 200 {
		t.Fatalf("PATCH status: %d", status)
	}
	if body["sources"].([]any)[0].(map[string]any)["enabled"] != false {
		t.Error("source should be disabled after PATCH")
	}
	body, status = e.mutate(t, "DELETE", "/api/sources", map[string]any{"key": key})
	if status != 200 {
		t.Fatalf("DELETE status: %d", status)
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
	addSource(t, e, "https://feeds.example.com/jobs.xml")

	before := e.get(t, "/api/status")["catalogue"].(float64)
	runCollect(t, e)
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

	setSources(t, e, "https://boards.greenhouse.io/acme", "https://feeds.example.com/empty.xml", "https://blocked.example.com/jobs", "https://noparse.example.com/jobs")

	runCollect(t, e)

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

func TestSourceStatusErrorText(t *testing.T) {
	cases := []struct {
		name         string
		firstOK      bool   // run a successful fetch first
		fetchErr     error  // error to inject on the (second) run
		wantContains string // substring the status text must contain
		wantHasPrior bool   // text must also contain "last success"
	}{
		{
			name:         "not found without prior success",
			fetchErr:     fetch.ErrNotFound,
			wantContains: "Not found",
		},
		{
			name:         "blocked after prior success",
			firstOK:      true,
			fetchErr:     fetch.ErrBlocked,
			wantContains: "Blocked by site",
			wantHasPrior: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			addSource(t, e, "https://feeds.example.com/jobs.xml")
			if tc.firstOK {
				e.fm.Set("https://feeds.example.com/jobs.xml",
					fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "application/xml", Body: []byte(feedXML)}})
				runCollect(t, e)
				srcs := sourcesOf(e.get(t, "/api/sources"))
				text := srcs[0]["status"].(map[string]any)["text"].(string)
				if !strings.HasPrefix(text, "Last success") {
					t.Fatalf("after success: text = %q, want prefix 'Last success'", text)
				}
			}
			e.fm.Set("https://feeds.example.com/jobs.xml", fetchtest.MapEntry{Err: tc.fetchErr})
			runCollect(t, e)
			srcs := sourcesOf(e.get(t, "/api/sources"))
			text := srcs[0]["status"].(map[string]any)["text"].(string)
			if !strings.Contains(text, tc.wantContains) {
				t.Errorf("text = %q, want to contain %q", text, tc.wantContains)
			}
			if tc.wantHasPrior && !strings.Contains(text, "last success") {
				t.Errorf("text = %q, want to contain 'last success'", text)
			}
		})
	}
}

func TestSourceStatusNoVacanciesIn30Days(t *testing.T) {
	var mu sync.Mutex
	nowTime := base
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return nowTime }

	e := newEnvClock(t, now, false)
	e.fm.Set("https://feeds.example.com/empty.xml",
		fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "application/xml", Body: []byte(emptyFeedXML)}})
	addSource(t, e, "https://feeds.example.com/empty.xml")

	// First run at base: source is empty
	runCollect(t, e)

	// Advance clock past 30 days
	mu.Lock()
	nowTime = base.Add(31 * 24 * time.Hour)
	mu.Unlock()

	// Second run: still empty
	runCollect(t, e)

	srcs := sourcesOf(e.get(t, "/api/sources"))
	text := srcs[0]["status"].(map[string]any)["text"].(string)
	if text != "No vacancies in 30 days" {
		t.Errorf("30-day rule: text = %q, want 'No vacancies in 30 days'", text)
	}
}

func TestAttentionThresholdAndCount(t *testing.T) {
	t.Run("triggers after three failures not two", func(t *testing.T) {
		e := newEnv(t)
		e.fm.Set("https://feeds.example.com/jobs.xml", fetchtest.MapEntry{Err: fetch.ErrBlocked})
		addSource(t, e, "https://feeds.example.com/jobs.xml")
		for run := 1; run <= 3; run++ {
			runCollect(t, e)
			srcs := sourcesOf(e.get(t, "/api/sources"))
			got := srcs[0]["status"].(map[string]any)["attention"].(bool)
			want := run >= 3
			if got != want {
				t.Errorf("after %d runs: attention = %v, want %v", run, got, want)
			}
		}
	})

	t.Run("count and text with two sources, singular after disable", func(t *testing.T) {
		e := newEnv(t)
		e.fm.Set("https://feeds.example.com/a.xml", fetchtest.MapEntry{Err: fetch.ErrBlocked})
		e.fm.Set("https://feeds2.example.com/b.xml", fetchtest.MapEntry{Err: fetch.ErrBlocked})
		setSources(t, e, "https://feeds.example.com/a.xml", "https://feeds2.example.com/b.xml")
		for i := 0; i < 3; i++ {
			runCollect(t, e)
		}
		st := e.get(t, "/api/status")
		att := st["attention"].(map[string]any)
		if att["count"].(float64) != 2 {
			t.Errorf("attention.count: got %v, want 2", att["count"])
		}
		if att["text"] != "2 sources need attention" {
			t.Errorf("attention.text: got %v, want '2 sources need attention'", att["text"])
		}
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
	})
}

// --- Detail fetch behaviour --------------------------------------------------

func htmlListing(u string) []byte {
	return []byte(`<html><body><a href="` + u + `">Senior Engineer</a></body></html>`)
}

func TestRunDetailFetchesTextForNewPostingWithoutText(t *testing.T) {
	e := newEnv(t)
	const vacURL = "https://generic.example.com/jobs/1"
	e.fm.Set("https://generic.example.com/jobs", fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "text/html", Body: htmlListing(vacURL)}})
	e.fm.Set(vacURL, fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "text/html", Body: []byte(`<html><head><script type="application/ld+json">{"@type":"JobPosting","title":"Senior Engineer","hiringOrganization":{"name":"Acme"},"url":"https://generic.example.com/jobs/1","description":"<p>Full text.</p>"}</script></head></html>`)}})
	addSource(t, e, "https://generic.example.com/jobs")
	runCollect(t, e)
	if n := len(e.fm.Requests()); n != 2 {
		t.Errorf("requests: got %d, want 2 (listing+detail)", n)
	}
	var state string
	e.db.QueryRow(`SELECT text_state FROM vacancy WHERE url = ?`, vacURL).Scan(&state) //nolint:errcheck
	if state != "full" {
		t.Errorf("text_state: got %q, want full", state)
	}
}

func TestRunDetailSkipsPostingWithText(t *testing.T) {
	e := newEnv(t)
	e.fm.Set("https://boards-api.greenhouse.io/v1/boards/acme/jobs?content=true",
		fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "application/json", Body: []byte(ghBoardResponse)}})
	addSource(t, e, "https://boards.greenhouse.io/acme")
	runCollect(t, e)
	if n := len(e.fm.Requests()); n != 1 {
		t.Errorf("requests: got %d, want 1 (listing only, no detail)", n)
	}
}

func TestRunDetailSkipsWithin7DaysAndRetriesAfter(t *testing.T) {
	var mu sync.Mutex
	nowTime := base
	e := newEnvClock(t, func() time.Time { mu.Lock(); defer mu.Unlock(); return nowTime }, false)
	const listURL, vacURL = "https://generic.example.com/jobs", "https://generic.example.com/jobs/2"
	e.fm.Set(listURL, fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "text/html", Body: htmlListing(vacURL)}})
	e.fm.Set(vacURL, fetchtest.MapEntry{Err: fetch.ErrNotFound})
	addSource(t, e, listURL)
	run := func(want int) {
		t.Helper()
		runCollect(t, e)
		if n := len(e.fm.Requests()); n != want {
			t.Errorf("requests: got %d, want %d", n, want)
		}
	}
	run(2) // first run: listing + detail
	mu.Lock()
	nowTime = base.Add(24 * time.Hour)
	mu.Unlock()
	run(3) // 1 day later: listing only (within 7-day window)
	mu.Lock()
	nowTime = base.Add(8 * 24 * time.Hour)
	mu.Unlock()
	run(5) // 8 days after first: listing + detail again
}

func TestRunRemovedAddressGetsNoRequest(t *testing.T) {
	e := newEnv(t)
	const vacURL = "https://generic.example.com/jobs/3"
	e.fm.Set("https://generic.example.com/jobs", fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "text/html", Body: htmlListing(vacURL)}})
	e.db.Exec(`INSERT INTO tombstone (key) VALUES ('url:` + vacURL + `')`) //nolint:errcheck
	addSource(t, e, "https://generic.example.com/jobs")
	runCollect(t, e)
	if n := len(e.fm.Requests()); n != 1 {
		t.Errorf("requests: got %d, want 1 (listing only)", n)
	}
}

func TestRunBlockedDetailSavesPostingAsBlocked(t *testing.T) {
	e := newEnv(t)
	const vacURL = "https://generic.example.com/jobs/4"
	e.fm.Set("https://generic.example.com/jobs", fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "text/html", Body: htmlListing(vacURL)}})
	e.fm.Set(vacURL, fetchtest.MapEntry{Err: fetch.ErrBlocked})
	addSource(t, e, "https://generic.example.com/jobs")
	runCollect(t, e)
	var state, reqAt string
	e.db.QueryRow(`SELECT text_state, detail_requested_at FROM vacancy WHERE url = ?`, vacURL).Scan(&state, &reqAt) //nolint:errcheck
	if state != "blocked" || reqAt != "" {
		t.Errorf("text_state=%q detail_requested_at=%q, want blocked and empty", state, reqAt)
	}
}

func TestSourceRunRowDeletedWithJob(t *testing.T) {
	e := newEnv(t)
	e.fm.Set("https://feeds.example.com/jobs.xml",
		fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "application/xml", Body: []byte(feedXML)}})
	addSource(t, e, "https://feeds.example.com/jobs.xml")

	j := runCollect(t, e)

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

// --- Exclusions --------------------------------------------------------------

// exclusionFeed is a minimal RSS feed with no body text so that needDetail=true.
const exclusionFeed = `<rss version="2.0"><channel>
<item><title>Go Developer</title><link>https://feeds.example.com/jobs/excl1</link></item>
</channel></rss>`

// exclusionDetailPage returns a JSON-LD job page revealing company "ExclCo".
const exclusionDetailPage = `<html><head><script type="application/ld+json">` +
	`{"@type":"JobPosting","title":"Go Developer","hiringOrganization":{"name":"ExclCo"},"description":"<p>text</p>"}` +
	`</script></head><body></body></html>`

func TestCollect_ExcludedBeforeDetail(t *testing.T) {
	e := newEnv(t)
	e.fm.Set("https://boards-api.greenhouse.io/v1/boards/acme/jobs?content=true",
		fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "application/json", Body: []byte(ghBoardResponse)}})
	addSource(t, e, "https://boards.greenhouse.io/acme")

	// Word exclusion for "engineer" — matches "Engineer" in the greenhouse board.
	e.db.Exec(`INSERT INTO exclusion (kind,value,norm,profile_id,created) VALUES ('word','engineer','engineer',0,datetime('now'))`) //nolint:errcheck

	runCollect(t, e)

	// Both postings are saved; "Engineer" is excluded from view but still upserted.
	var cnt int
	e.db.QueryRow(`SELECT count(*) FROM vacancy`).Scan(&cnt) //nolint:errcheck
	if cnt != 2 {
		t.Errorf("catalog: got %d vacancies, want 2 (both saved, Engineer excluded from view)", cnt)
	}
	var excl int
	e.db.QueryRow(`SELECT excluded FROM source_run ORDER BY job_id DESC LIMIT 1`).Scan(&excl) //nolint:errcheck
	if excl != 1 {
		t.Errorf("source_run.excluded: got %d, want 1", excl)
	}
}

func TestCollect_ExcludedAfterDetail(t *testing.T) {
	e := newEnv(t)
	e.fm.Set("https://feeds.example.com/excl.xml",
		fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "application/xml", Body: []byte(exclusionFeed)}})
	e.fm.Set("https://feeds.example.com/jobs/excl1",
		fetchtest.MapEntry{Response: fetch.Response{Status: 200, ContentType: "text/html", Body: []byte(exclusionDetailPage)}})
	addSource(t, e, "https://feeds.example.com/excl.xml")

	// Company exclusion for "ExclCo" — not visible in the listing, only after Detail.
	e.db.Exec(`INSERT INTO exclusion (kind,value,norm,profile_id,created) VALUES ('company','ExclCo','exclco',0,datetime('now'))`) //nolint:errcheck

	runCollect(t, e)

	// The posting is saved with detail text; it is excluded from view but still upserted.
	var cnt int
	e.db.QueryRow(`SELECT count(*) FROM vacancy`).Scan(&cnt) //nolint:errcheck
	if cnt != 1 {
		t.Errorf("catalog: got %d vacancies, want 1 (saved with detail text, excluded from view)", cnt)
	}
	var excl int
	e.db.QueryRow(`SELECT excluded FROM source_run ORDER BY job_id DESC LIMIT 1`).Scan(&excl) //nolint:errcheck
	if excl != 1 {
		t.Errorf("source_run.excluded: got %d, want 1", excl)
	}
}
