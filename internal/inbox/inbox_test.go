package inbox_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sifty-works/sifty/internal/catalog"
	"github.com/sifty-works/sifty/internal/db"
	"github.com/sifty-works/sifty/internal/inbox"
	"github.com/sifty-works/sifty/internal/listing"
	"github.com/sifty-works/sifty/internal/server"
)

// testNow is 17 Nov 2026 12:00 UTC, the fixed "now" for all tests.
var testNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func testNowFn() time.Time { return testNow }

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"), db.Migrations)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// newCat returns a catalog whose clock is after all test publication dates.
func newCat(d *db.DB) *catalog.Catalog {
	return catalog.New(d, func() time.Time { return time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC) })
}

func newTestServer(t *testing.T, d *db.DB) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := server.New(server.Options{Version: "test", Port: 9999, Logger: logger},
		func(mux *http.ServeMux) {
			inbox.RegisterRoutes(mux, d, testNowFn, logger)
		})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func apiGet(t *testing.T, ts *httptest.Server, path string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	req.Host = "127.0.0.1:9999"
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", path, resp.StatusCode)
	}
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body) //nolint:errcheck
	return body
}

func getRows(body map[string]any) []any {
	r, _ := body["rows"].([]any)
	return r
}

func field(r any, key string) string {
	m, _ := r.(map[string]any)
	v, _ := m[key].(string)
	return v
}

func rowID(r any) float64 {
	m, _ := r.(map[string]any)
	v, _ := m["id"].(float64)
	return v
}

func add(t *testing.T, cat *catalog.Catalog, p listing.Posting) int64 {
	t.Helper()
	id, _, err := cat.Upsert(context.Background(), p, "src")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	return id
}

func post(url, title, company string) listing.Posting {
	return listing.Posting{
		URL: url, Title: title, Company: company,
		Text: "job description", TextKind: listing.Plain, TextState: listing.TextFull,
	}
}

// --- Rows and fields ---

func TestRows_AllFieldsPresent(t *testing.T) {
	d := openTestDB(t)
	add(t, newCat(d), listing.Posting{
		URL: "https://example.com/jobs/1", Title: "Backend Engineer",
		Company: "Acme Corp", Location: "Remote · EU",
		Published: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), PublishedBasis: "datePosted",
		Text: "Full description.", TextKind: listing.Plain, TextState: listing.TextFull,
	})
	body := apiGet(t, newTestServer(t, d), "/api/vacancies")
	rs := getRows(body)
	if len(rs) != 1 {
		t.Fatalf("rows: got %d, want 1", len(rs))
	}
	r := rs[0]
	for key, want := range map[string]string{
		"company":  "Acme Corp",
		"title":    "Backend Engineer",
		"location": "Remote · EU",
	} {
		if field(r, key) != want {
			t.Errorf("%s: got %q, want %q", key, field(r, key), want)
		}
	}
	if field(r, "published") == "" {
		t.Error("published should not be empty")
	}
	if total, _ := body["total"].(float64); total != 1 {
		t.Errorf("total: got %v, want 1", total)
	}
	if empty, _ := body["empty"].(string); empty != "" {
		t.Errorf("empty: got %q, want empty string", empty)
	}

	// A vacancy without a location shows "Location unstated".
	add(t, newCat(d), post("https://example.com/jobs/2", "Engineer No Location", "Acme"))
	rs = getRows(apiGet(t, newTestServer(t, d), "/api/vacancies"))
	// The no-location vacancy has no published date so it sorts last (rank 3).
	if got := field(rs[1], "location"); got != "Location unstated" {
		t.Errorf("no-location vacancy: got %q, want %q", got, "Location unstated")
	}
}

// --- Publication line and age forms ---

func TestPublicationLine_Forms(t *testing.T) {
	cases := []struct {
		name    string
		pubAt   time.Time
		basis   string
		wantPub string
	}{
		{
			"with basis 15 days ago",
			time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), "datePosted",
			"Published 2 Nov 2026 · 2 weeks",
		},
		{
			"without basis",
			time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), "",
			"Source date 15 Oct 2026 (unverified)",
		},
		{
			"today",
			time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC), "datePosted",
			"Published 17 Nov 2026 · today",
		},
		{
			"1 day",
			time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC), "datePosted",
			"Published 16 Nov 2026 · 1 day",
		},
		{
			"5 days",
			time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC), "datePosted",
			"Published 12 Nov 2026 · 5 days",
		},
		{
			"3 weeks",
			time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC), "datePosted",
			"Published 27 Oct 2026 · 3 weeks",
		},
		{
			"3 months (90 days)",
			time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC), "datePosted",
			"Published 19 Aug 2026 · 3 months",
		},
		{"no date", time.Time{}, "", "Publication date not reported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := openTestDB(t)
			p := post("https://example.com/jobs/pl-"+tc.name, "Engineer", "Acme")
			p.Published = tc.pubAt
			p.PublishedBasis = tc.basis
			add(t, newCat(d), p)
			body := apiGet(t, newTestServer(t, d), "/api/vacancies")
			rs := getRows(body)
			if len(rs) != 1 {
				t.Fatalf("rows: %d", len(rs))
			}
			if got := field(rs[0], "published"); got != tc.wantPub {
				t.Errorf("published: got %q, want %q", got, tc.wantPub)
			}
		})
	}
}

// --- Newest order: three ranks and tie-breaks ---

func TestOrder_ThreeRanks(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	// Rank 2: unverified date, newer absolute date.
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/ord1", Title: "Rank2", Company: "Co",
		Published: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC),
		Text:      "job", TextKind: listing.Plain, TextState: listing.TextFull,
	})
	// Rank 1: basis date, 5 Nov (older than rank-2 absolute date but rank wins).
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/ord2", Title: "Rank1 Newer", Company: "Co",
		Published: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC), PublishedBasis: "datePosted",
		Text: "job", TextKind: listing.Plain, TextState: listing.TextFull,
	})
	// Rank 3: no date.
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/ord3", Title: "Rank3", Company: "Co",
		Text: "job", TextKind: listing.Plain, TextState: listing.TextFull,
	})
	// Rank 1: basis date, 1 Nov (older within rank-1).
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/ord4", Title: "Rank1 Older", Company: "Co",
		Published: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), PublishedBasis: "datePosted",
		Text: "job", TextKind: listing.Plain, TextState: listing.TextFull,
	})
	body := apiGet(t, newTestServer(t, d), "/api/vacancies")
	rs := getRows(body)
	if len(rs) != 4 {
		t.Fatalf("rows: %d", len(rs))
	}
	wantTitles := []string{"Rank1 Newer", "Rank1 Older", "Rank2", "Rank3"}
	for i, want := range wantTitles {
		if got := field(rs[i], "title"); got != want {
			t.Errorf("row[%d]: got %q, want %q", i, got, want)
		}
	}
}

func TestOrder_TieBreakByFirstSeenThenID(t *testing.T) {
	d := openTestDB(t)
	t1 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	add(t, catalog.New(d, func() time.Time { return t1 }),
		post("https://example.com/jobs/tie1", "Tie Early", "Co"))
	id2 := add(t, catalog.New(d, func() time.Time { return t2 }),
		post("https://example.com/jobs/tie2", "Tie Later", "Co"))
	rs := getRows(apiGet(t, newTestServer(t, d), "/api/vacancies"))
	if len(rs) < 2 {
		t.Fatalf("rows: %d", len(rs))
	}
	if rowID(rs[0]) != float64(id2) {
		t.Errorf("first row: id %v, want %v (later first_seen)", rowID(rs[0]), id2)
	}
}

// --- Search ---

func TestSearch(t *testing.T) {
	// Shared DB with four vacancies covering all search behaviours.
	d := openTestDB(t)
	cat := newCat(d)
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/s1", Title: "Golang Backend", Company: "Acme Corp",
		Tags: []string{"Kubernetes"}, Text: "orchestration experience", TextKind: listing.Plain, TextState: listing.TextFull,
	})
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/s2", Title: "Frontend Designer", Company: "DeepMind AI",
		Tags: []string{"Figma"}, Text: "knowledge of color theory", TextKind: listing.Plain, TextState: listing.TextFull,
	})
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/s3", Title: "TypeScript Engineer", Company: "Acme",
		Text: "Must have OBSERVABILITY and distributed tracing systems", TextKind: listing.Plain, TextState: listing.TextFull,
	})
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/s4", Title: "Platform Engineer", Company: "Acme",
		TextState: listing.TextNotFetched,
	})
	ts := newTestServer(t, d)

	// By title, company and tag.
	for _, tc := range []struct {
		q, wantTitle string
	}{
		{"golang", "Golang Backend"},
		{"deepmind", "Frontend Designer"},
		{"kubernetes", "Golang Backend"},
	} {
		rs := getRows(apiGet(t, ts, "/api/vacancies?q="+tc.q))
		if len(rs) != 1 || field(rs[0], "title") != tc.wantTitle {
			t.Errorf("q=%q: rows %d, title %q", tc.q, len(rs), field(rs[0], "title"))
		}
	}

	// Total counts all vacancies regardless of query.
	if total, _ := apiGet(t, ts, "/api/vacancies?q=golang")["total"].(float64); total != 4 {
		t.Errorf("total with query: %v, want 4", total)
	}

	// By body text.
	rs := getRows(apiGet(t, ts, "/api/vacancies?q=distributed+tracing"))
	if len(rs) != 1 || field(rs[0], "title") != "TypeScript Engineer" {
		t.Errorf("body search: rows %d, title %q", len(rs), field(rs[0], "title"))
	}

	// A query shorter than 3 characters skips FTS.
	if rs := getRows(apiGet(t, ts, "/api/vacancies?q=di")); len(rs) != 0 {
		t.Errorf("2-char query: rows %d, want 0", len(rs))
	}

	// Case-insensitive matching.
	for _, q := range []string{"typescript", "TYPESCRIPT", "observability", "OBSERVABILITY"} {
		if rs := getRows(apiGet(t, ts, "/api/vacancies?q="+q)); len(rs) != 1 {
			t.Errorf("q=%q: rows %d, want 1", q, len(rs))
		}
	}

	// A no-body vacancy is found by title but not by a body-only term.
	if rs := getRows(apiGet(t, ts, "/api/vacancies?q=platform")); len(rs) != 1 || field(rs[0], "title") != "Platform Engineer" {
		t.Errorf("title search on no-body: rows %d, title %q", len(rs), field(rs[0], "title"))
	}
	// "observability" matches s3's body; s4 (no body) must not appear.
	if rs := getRows(apiGet(t, ts, "/api/vacancies?q=observability")); len(rs) != 1 || field(rs[0], "title") != "TypeScript Engineer" {
		t.Errorf("body term: rows %d, title %q; want 1 hit on TypeScript Engineer only", len(rs), field(rs[0], "title"))
	}

	// Empty or whitespace-only query returns all vacancies.
	for _, q := range []string{"", "+++"} {
		if rs := getRows(apiGet(t, ts, "/api/vacancies?q="+q)); len(rs) != 4 {
			t.Errorf("q=%q: rows %d, want 4", q, len(rs))
		}
	}

	// Updated text replaces old text in search: old term not found, new term found.
	_, _, err := newCat(d).Upsert(context.Background(), listing.Posting{
		URL: "https://example.com/jobs/s3", Title: "TypeScript Engineer", Company: "Acme",
		Text:      "Now uses TRACING_REPLACED instead",
		TextKind:  listing.Plain,
		TextState: listing.TextFull,
	}, "src")
	if err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	if rs := getRows(apiGet(t, ts, "/api/vacancies?q=observability")); len(rs) != 0 {
		t.Errorf("after text update: old term still found, want 0 hits")
	}
	if rs := getRows(apiGet(t, ts, "/api/vacancies?q=tracing_replaced")); len(rs) != 1 {
		t.Errorf("after text update: new term not found, want 1 hit")
	}

	// Merged vacancy: search finds kept text; merged-away rows are not returned.
	cat2 := newCat(d)
	add(t, cat2, listing.Posting{
		URL: "https://other.com/jobs/merge1", Title: "Golang Backend", Company: "Acme Corp",
		Text:     "merge_term_xyz and distributed systems",
		TextKind: listing.Plain, TextState: listing.TextFull,
	})
	if rs := getRows(apiGet(t, ts, "/api/vacancies?q=merge_term_xyz")); len(rs) != 1 {
		t.Errorf("after merge: search should find one result, got %d", len(rs))
	}
}

// apiJSON sends a request with method and JSON body, returning decoded body and status code.
func apiJSON(t *testing.T, ts *httptest.Server, method, path string, body any) (map[string]any, int) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, ts.URL+path, bytes.NewReader(b))
	req.Host = "127.0.0.1:9999"
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out) //nolint:errcheck
	return out, resp.StatusCode
}

// --- Exclusions ---

func TestExclusions_CRUD(t *testing.T) {
	d := openTestDB(t)
	ts := newTestServer(t, d)

	body := apiGet(t, ts, "/api/exclusions")
	if excls, _ := body["exclusions"].([]any); len(excls) != 0 {
		t.Fatalf("initial list: want 0, got %d", len(excls))
	}

	body, status := apiJSON(t, ts, http.MethodPost, "/api/exclusions", map[string]any{"kind": "word", "value": "kotlin"})
	if status != http.StatusOK {
		t.Fatalf("POST: status %d", status)
	}
	excls, _ := body["exclusions"].([]any)
	if len(excls) != 1 {
		t.Fatalf("after add: want 1, got %d", len(excls))
	}
	row, _ := excls[0].(map[string]any)
	if row["scope"] != "All profiles" {
		t.Errorf("scope: got %v", row["scope"])
	}
	id := row["id"].(float64)

	body, status = apiJSON(t, ts, http.MethodDelete, "/api/exclusions", map[string]any{"id": id})
	if status != http.StatusOK {
		t.Fatalf("DELETE: status %d", status)
	}
	if excls, _ := body["exclusions"].([]any); len(excls) != 0 {
		t.Fatalf("after delete: want 0, got %d", len(excls))
	}
}

func TestExclusions_Validation(t *testing.T) {
	d := openTestDB(t)
	ts := newTestServer(t, d)
	// duplicate word returns ok, still one entry
	apiJSON(t, ts, http.MethodPost, "/api/exclusions", map[string]any{"kind": "word", "value": "ruby"})
	body, status := apiJSON(t, ts, http.MethodPost, "/api/exclusions", map[string]any{"kind": "word", "value": "ruby"})
	if status != http.StatusOK {
		t.Fatalf("duplicate: status %d", status)
	}
	if excls, _ := body["exclusions"].([]any); len(excls) != 1 {
		t.Fatalf("duplicate: want 1, got %d", len(excls))
	}
	// unknown company returns 422
	_, status = apiJSON(t, ts, http.MethodPost, "/api/exclusions", map[string]any{"kind": "company", "value": "Unknown"})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("Unknown company: status %d, want 422", status)
	}
}

func TestExclusions_Preview(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	add(t, cat, post("https://example.com/jobs/pv1", "Rust Engineer", "Zephyr"))
	add(t, cat, post("https://example.com/jobs/pv2", "Go Developer", "Globex"))
	ts := newTestServer(t, d)

	for _, tc := range []struct{ kind, value, want string }{
		{"word", "rust", "Would hide 1 role"},
		{"word", "engineer", "Would hide 1 role"},
		{"word", "xyznothing", "Would hide 0 roles"},
		{"company", "Zephyr", "Would hide 1 role"},
		{"company", "Globex", "Would hide 1 role"},
	} {
		body := apiGet(t, ts, "/api/exclusions/preview?kind="+tc.kind+"&value="+tc.value)
		if text, _ := body["text"].(string); text != tc.want {
			t.Errorf("preview kind=%s value=%s: got %q, want %q", tc.kind, tc.value, text, tc.want)
		}
	}
}

func TestExclusions_Filter(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	add(t, cat, post("https://example.com/jobs/ex1", "Frontend Developer", "Globex"))
	add(t, cat, post("https://example.com/jobs/ex2", "Backend Engineer", "Acme Corp"))
	// Greenhouse board-name rule: apply_url board name differs from stored company.
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/ex3", Title: "Staff Engineer", Company: "Widget Co",
		ApplyURL: "https://boards.greenhouse.io/widgetboard/jobs/999",
		Text:     "job", TextKind: listing.Plain, TextState: listing.TextFull,
	})
	ts := newTestServer(t, d)

	check := func(label string, wantRows int, wantTotal float64) {
		t.Helper()
		lb := apiGet(t, ts, "/api/vacancies")
		if rs := getRows(lb); len(rs) != wantRows {
			t.Errorf("%s: rows %d, want %d", label, len(rs), wantRows)
		}
		if total, _ := lb["total"].(float64); total != wantTotal {
			t.Errorf("%s: total %v, want %v", label, total, wantTotal)
		}
	}

	check("initial", 3, 3)

	// Word exclusion hides matching title; search also returns nothing.
	body, _ := apiJSON(t, ts, http.MethodPost, "/api/exclusions", map[string]any{"kind": "word", "value": "frontend"})
	id1 := body["exclusions"].([]any)[0].(map[string]any)["id"].(float64)
	check("word filter", 2, 2)
	if rs := getRows(apiGet(t, ts, "/api/vacancies?q=frontend")); len(rs) != 0 {
		t.Errorf("word search: rows %d, want 0", len(rs))
	}
	apiJSON(t, ts, http.MethodDelete, "/api/exclusions", map[string]any{"id": id1})
	check("after word remove", 3, 3)

	// Company exclusion hides vacancy by normalised identity.
	body, _ = apiJSON(t, ts, http.MethodPost, "/api/exclusions", map[string]any{"kind": "company", "value": "Acme Corp"})
	id2 := body["exclusions"].([]any)[0].(map[string]any)["id"].(float64)
	check("company filter", 2, 2)
	apiJSON(t, ts, http.MethodDelete, "/api/exclusions", map[string]any{"id": id2})
	check("after company remove", 3, 3)

	// Greenhouse board-name rule: "widgetboard" does not match "Widget Co" by identity
	// but matches via apply_url containing .greenhouse.io/widgetboard/jobs/.
	body, _ = apiJSON(t, ts, http.MethodPost, "/api/exclusions", map[string]any{"kind": "company", "value": "widgetboard"})
	check("greenhouse rule", 2, 2)
}

func TestExclusions_UnknownPlaceholderNotMatchedByWord(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	// A vacancy with no company (empty placeholder, displayed as "Unknown").
	add(t, cat, post("https://example.com/jobs/unk1", "Frontend Developer", ""))
	// A vacancy whose title contains "unknown" should still be hidden.
	add(t, cat, post("https://example.com/jobs/unk2", "Unknown Role", "Nexus"))
	ts := newTestServer(t, d)

	apiJSON(t, ts, http.MethodPost, "/api/exclusions", map[string]any{"kind": "word", "value": "unknown"})

	rs := getRows(apiGet(t, ts, "/api/vacancies"))
	if len(rs) != 1 {
		t.Fatalf("rows: got %d, want 1 (placeholder company not matched by word exclusion)", len(rs))
	}
}

// --- Empty states ---

func TestEmpty(t *testing.T) {
	d := openTestDB(t)
	ts := newTestServer(t, d)
	body := apiGet(t, ts, "/api/vacancies")
	if empty, _ := body["empty"].(string); empty != "Inbox zero. Collect more jobs in Settings → Sources." {
		t.Errorf("empty text: got %q", empty)
	}
	if total, _ := body["total"].(float64); total != 0 {
		t.Errorf("total: %v", total)
	}
	add(t, newCat(d), post("https://example.com/jobs/em1", "Engineer", "Acme"))
	body = apiGet(t, ts, "/api/vacancies?q=zzznomatch")
	if empty, _ := body["empty"].(string); empty != "No matching roles. Adjust your filters." {
		t.Errorf("empty text: got %q", empty)
	}
	if total, _ := body["total"].(float64); total != 1 {
		t.Errorf("total: %v, want 1", total)
	}
}
