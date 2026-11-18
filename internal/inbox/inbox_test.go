package inbox_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func newCat(d *db.DB) *catalog.Catalog {
	return catalog.New(d, func() time.Time { return time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC) })
}

func newTestServer(t *testing.T, d *db.DB) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := server.New(server.Options{Version: "test", Port: 9999, Logger: logger},
		func(mux *http.ServeMux) {
			inbox.RegisterRoutes(mux, d, testNowFn, logger, func() bool { return false })
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

func countOf(body map[string]any, stage string) float64 {
	c, _ := body["counts"].(map[string]any)
	n, _ := c[stage].(float64)
	return n
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
	rs := getRows(apiGet(t, newTestServer(t, d), "/api/vacancies"))
	if len(rs) != 1 {
		t.Fatalf("rows: got %d, want 1", len(rs))
	}
	r := rs[0]
	for key, want := range map[string]string{
		"company": "Acme Corp", "title": "Backend Engineer", "location": "Remote · EU",
	} {
		if field(r, key) != want {
			t.Errorf("%s: got %q, want %q", key, field(r, key), want)
		}
	}
	if field(r, "published") == "" {
		t.Error("published should not be empty")
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
		{"with basis 15 days ago", time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), "datePosted", "Published 2 Nov 2026 · 2 weeks"},
		{"without basis", time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), "", "Source date 15 Oct 2026 (unverified)"},
		{"today", time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC), "datePosted", "Published 17 Nov 2026 · today"},
		{"1 day", time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC), "datePosted", "Published 16 Nov 2026 · 1 day"},
		{"5 days", time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC), "datePosted", "Published 12 Nov 2026 · 5 days"},
		{"3 weeks", time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC), "datePosted", "Published 27 Oct 2026 · 3 weeks"},
		{"3 months (90 days)", time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC), "datePosted", "Published 19 Aug 2026 · 3 months"},
		{"no date", time.Time{}, "", "Publication date not reported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := openTestDB(t)
			p := post("https://example.com/jobs/pl-"+tc.name, "Engineer", "Acme")
			p.Published = tc.pubAt
			p.PublishedBasis = tc.basis
			add(t, newCat(d), p)
			rs := getRows(apiGet(t, newTestServer(t, d), "/api/vacancies"))
			if len(rs) != 1 {
				t.Fatalf("rows: %d", len(rs))
			}
			if got := field(rs[0], "published"); got != tc.wantPub {
				t.Errorf("published: got %q, want %q", got, tc.wantPub)
			}
		})
	}
}

// --- Newest order ---

func TestOrder_ThreeRanks(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	for _, p := range []listing.Posting{
		{URL: "https://example.com/jobs/ord1", Title: "Rank2", Company: "Co",
			Published: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC),
			Text:      "job", TextKind: listing.Plain, TextState: listing.TextFull},
		{URL: "https://example.com/jobs/ord2", Title: "Rank1 Newer", Company: "Co",
			Published: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC), PublishedBasis: "datePosted",
			Text: "job", TextKind: listing.Plain, TextState: listing.TextFull},
		{URL: "https://example.com/jobs/ord3", Title: "Rank3", Company: "Co",
			Text: "job", TextKind: listing.Plain, TextState: listing.TextFull},
		{URL: "https://example.com/jobs/ord4", Title: "Rank1 Older", Company: "Co",
			Published: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), PublishedBasis: "datePosted",
			Text: "job", TextKind: listing.Plain, TextState: listing.TextFull},
	} {
		add(t, cat, p)
	}
	rs := getRows(apiGet(t, newTestServer(t, d), "/api/vacancies"))
	if len(rs) != 4 {
		t.Fatalf("rows: %d", len(rs))
	}
	for i, want := range []string{"Rank1 Newer", "Rank1 Older", "Rank2", "Rank3"} {
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

	for _, tc := range []struct{ q, want string }{
		{"golang", "Golang Backend"}, {"deepmind", "Frontend Designer"}, {"kubernetes", "Golang Backend"},
	} {
		rs := getRows(apiGet(t, ts, "/api/vacancies?q="+tc.q))
		if len(rs) != 1 || field(rs[0], "title") != tc.want {
			t.Errorf("q=%q: rows %d, title %q", tc.q, len(rs), field(rs[0], "title"))
		}
	}

	// counts ignores q.
	if countOf(apiGet(t, ts, "/api/vacancies?q=golang"), "inbox") != 4 {
		t.Errorf("counts.inbox with query: want 4")
	}

	rs := getRows(apiGet(t, ts, "/api/vacancies?q=distributed+tracing"))
	if len(rs) != 1 || field(rs[0], "title") != "TypeScript Engineer" {
		t.Errorf("body search: rows %d, title %q", len(rs), field(rs[0], "title"))
	}

	if rs := getRows(apiGet(t, ts, "/api/vacancies?q=di")); len(rs) != 0 {
		t.Errorf("2-char query: rows %d, want 0", len(rs))
	}

	for _, q := range []string{"typescript", "TYPESCRIPT", "observability", "OBSERVABILITY"} {
		if rs := getRows(apiGet(t, ts, "/api/vacancies?q="+q)); len(rs) != 1 {
			t.Errorf("q=%q: rows %d, want 1", q, len(rs))
		}
	}

	if rs := getRows(apiGet(t, ts, "/api/vacancies?q=platform")); len(rs) != 1 || field(rs[0], "title") != "Platform Engineer" {
		t.Errorf("title search on no-body: rows %d", len(rs))
	}
	for _, q := range []string{"", "+++"} {
		if rs := getRows(apiGet(t, ts, "/api/vacancies?q="+q)); len(rs) != 4 {
			t.Errorf("q=%q: rows %d, want 4", q, len(rs))
		}
	}
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
	apiJSON(t, ts, http.MethodPost, "/api/exclusions", map[string]any{"kind": "word", "value": "ruby"})
	body, status := apiJSON(t, ts, http.MethodPost, "/api/exclusions", map[string]any{"kind": "word", "value": "ruby"})
	if status != http.StatusOK {
		t.Fatalf("duplicate: status %d", status)
	}
	if excls, _ := body["exclusions"].([]any); len(excls) != 1 {
		t.Fatalf("duplicate: want 1, got %d", len(excls))
	}
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
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/ex3", Title: "Staff Engineer", Company: "Widget Co",
		ApplyURL: "https://boards.greenhouse.io/widgetboard/jobs/999",
		Text:     "job", TextKind: listing.Plain, TextState: listing.TextFull,
	})
	ts := newTestServer(t, d)
	rows := func() int { return len(getRows(apiGet(t, ts, "/api/vacancies"))) }
	if rows() != 3 {
		t.Fatalf("initial: rows %d, want 3", rows())
	}
	body, _ := apiJSON(t, ts, http.MethodPost, "/api/exclusions", map[string]any{"kind": "word", "value": "frontend"})
	id1 := body["exclusions"].([]any)[0].(map[string]any)["id"].(float64)
	if rows() != 2 {
		t.Errorf("word filter: rows %d, want 2", rows())
	}
	if rs := getRows(apiGet(t, ts, "/api/vacancies?q=frontend")); len(rs) != 0 {
		t.Errorf("word search after filter: rows %d, want 0", len(rs))
	}
	apiJSON(t, ts, http.MethodDelete, "/api/exclusions", map[string]any{"id": id1})
	if rows() != 3 {
		t.Errorf("after word remove: rows %d", rows())
	}

	body, _ = apiJSON(t, ts, http.MethodPost, "/api/exclusions", map[string]any{"kind": "company", "value": "Acme Corp"})
	id2 := body["exclusions"].([]any)[0].(map[string]any)["id"].(float64)
	if rows() != 2 {
		t.Errorf("company filter: rows %d", rows())
	}
	apiJSON(t, ts, http.MethodDelete, "/api/exclusions", map[string]any{"id": id2})

	apiJSON(t, ts, http.MethodPost, "/api/exclusions", map[string]any{"kind": "company", "value": "widgetboard"})
	if rows() != 2 {
		t.Errorf("greenhouse rule: rows %d", rows())
	}
}

func TestExclusions_UnknownPlaceholderNotMatchedByWord(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	add(t, cat, post("https://example.com/jobs/unk1", "Frontend Developer", ""))
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
	if countOf(body, "inbox") != 0 {
		t.Errorf("counts.inbox: want 0")
	}
	add(t, newCat(d), post("https://example.com/jobs/em1", "Engineer", "Acme"))
	body = apiGet(t, ts, "/api/vacancies?q=zzznomatch")
	if empty, _ := body["empty"].(string); empty != "No matching roles. Adjust your filters." {
		t.Errorf("empty text: got %q", empty)
	}
	if countOf(body, "inbox") != 1 {
		t.Errorf("counts.inbox with query: want 1")
	}
	for stage, want := range map[string]string{
		"shortlist": "Nothing shortlisted yet — press Y in Inbox.",
		"applied":   "No applications yet.",
		"dismissed": "No dismissed roles.",
	} {
		body = apiGet(t, ts, "/api/vacancies?stage="+stage)
		if empty, _ := body["empty"].(string); empty != want {
			t.Errorf("empty %s: got %q, want %q", stage, empty, want)
		}
	}
}

// --- Four lists and counts ---

func TestListStages_CountsAndRows(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	id1 := add(t, cat, post("https://example.com/jobs/st1", "Alpha", "Co"))
	id2 := add(t, cat, post("https://example.com/jobs/st2", "Beta", "Co"))
	id3 := add(t, cat, post("https://example.com/jobs/st3", "Gamma", "Co"))
	id4 := add(t, cat, post("https://example.com/jobs/st4", "Delta", "Co"))
	ts := newTestServer(t, d)
	now := time.Now().UTC().Format(time.RFC3339)

	decide := func(vacID int64, stage, reason string) {
		d.Exec(fmt.Sprintf( //nolint:errcheck
			`INSERT OR REPLACE INTO decision (profile,vacancy,stage,reason,decided_at) VALUES (1,%d,'%s','%s','%s')`,
			vacID, stage, reason, now))
	}
	decide(id2, "shortlist", "")
	decide(id3, "applied", "")
	decide(id4, "dismissed", "got rejected")

	body := apiGet(t, ts, "/api/vacancies?stage=inbox")
	if rs := getRows(body); len(rs) != 1 || rowID(rs[0]) != float64(id1) {
		t.Errorf("inbox: rows %d, id %v", len(rs), rowID(rs[0]))
	}
	if countOf(body, "inbox") != 1 || countOf(body, "shortlist") != 1 ||
		countOf(body, "applied") != 1 || countOf(body, "dismissed") != 1 || countOf(body, "archive") != 2 {
		t.Errorf("counts mismatch: %v", body["counts"])
	}
	if rs := getRows(apiGet(t, ts, "/api/vacancies?stage=shortlist")); len(rs) != 1 || rowID(rs[0]) != float64(id2) {
		t.Errorf("shortlist: rows %d", len(rs))
	}
	rs := getRows(apiGet(t, ts, "/api/vacancies?stage=applied"))
	if len(rs) != 1 || field(rs[0], "stage") != "applied" {
		t.Errorf("applied: rows %d, stage %q", len(rs), field(rs[0], "stage"))
	}
	rs = getRows(apiGet(t, ts, "/api/vacancies?stage=dismissed"))
	if len(rs) != 1 || field(rs[0], "reason") != "got rejected" {
		t.Errorf("dismissed: rows %d, reason %q", len(rs), field(rs[0], "reason"))
	}
}

func TestListStages_AppliedOrder_AndAppliedText(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	id1 := add(t, cat, post("https://example.com/jobs/ao1", "Older", "Co"))
	id2 := add(t, cat, post("https://example.com/jobs/ao2", "Newer", "Co"))
	ts := newTestServer(t, d)

	d.Exec(`INSERT OR REPLACE INTO decision (profile,vacancy,stage,reason,decided_at) VALUES (1,?,?,?,?)`, //nolint:errcheck
		id1, "applied", "", "2026-11-01T12:00:00Z")
	d.Exec(`INSERT OR REPLACE INTO decision (profile,vacancy,stage,reason,decided_at) VALUES (1,?,?,?,?)`, //nolint:errcheck
		id2, "applied", "", "2026-11-10T12:00:00Z")
	d.Exec(`INSERT INTO application (vacancy,applied_at,profile,profile_name) VALUES (?,?,?,?)`, //nolint:errcheck
		id1, "2026-11-01T12:00:00Z", 1, "Alpha Profile")

	rs := getRows(apiGet(t, ts, "/api/vacancies?stage=applied"))
	if len(rs) != 2 || field(rs[0], "title") != "Newer" {
		t.Errorf("applied order: first title %q, want Newer", field(rs[0], "title"))
	}

	// Applied text in applied list: "Applied <date>" (no profile name)
	applied := field(rs[1], "applied")
	if applied != "Applied 1 Nov 2026" {
		t.Errorf("applied text in applied list: got %q, want 'Applied 1 Nov 2026'", applied)
	}
	// Applied text in inbox: "Applied <date> from <name>" when application exists.
	id3 := add(t, cat, post("https://example.com/jobs/ao3", "Third", "Co"))
	d.Exec(`INSERT INTO application (vacancy,applied_at,profile,profile_name) VALUES (?,?,?,?)`, //nolint:errcheck
		id3, "2026-10-01T12:00:00Z", 1, "Beta Profile")
	rs = getRows(apiGet(t, ts, "/api/vacancies?stage=inbox"))
	var found string
	for _, r := range rs {
		if rowID(r) == float64(id3) {
			found = field(r, "applied")
		}
	}
	if found != "Applied 1 Oct 2026 from Beta Profile" {
		t.Errorf("applied text in inbox: got %q, want 'Applied 1 Oct 2026 from Beta Profile'", found)
	}
}

func TestListStages_ExclusionsApplyToAllStages(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	id1 := add(t, cat, post("https://example.com/jobs/ea1", "Kotlin Dev", "Co"))
	add(t, cat, post("https://example.com/jobs/ea2", "Go Dev", "Co"))
	ts := newTestServer(t, d)
	now := time.Now().UTC().Format(time.RFC3339)
	d.Exec(`INSERT OR REPLACE INTO decision (profile,vacancy,stage,reason,decided_at) VALUES (1,?,?,?,?)`, //nolint:errcheck
		id1, "shortlist", "", now)
	apiJSON(t, ts, http.MethodPost, "/api/exclusions", map[string]any{"kind": "word", "value": "kotlin"})

	// Exclusion hides the kotlin vacancy in its stage list and in counts.
	if rs := getRows(apiGet(t, ts, "/api/vacancies?stage=shortlist")); len(rs) != 0 {
		t.Errorf("shortlist after exclusion: got %d rows, want 0", len(rs))
	}
	body := apiGet(t, ts, "/api/vacancies?stage=inbox")
	if countOf(body, "shortlist") != 0 {
		t.Errorf("counts.shortlist after exclusion: want 0")
	}
}

// --- Decision endpoint ---

func TestDecision_AllowedMoves(t *testing.T) {
	for i, tc := range []struct {
		from, to, reason string
		confirm          bool
	}{
		{"inbox", "shortlist", "", false},
		{"inbox", "dismissed", "", false},
		{"shortlist", "inbox", "", false},
		{"shortlist", "dismissed", "not a fit", false},
		{"shortlist", "applied", "", true},
		{"applied", "shortlist", "", false},
		{"dismissed", "inbox", "", false},
	} {
		t.Run(tc.from+"→"+tc.to, func(t *testing.T) {
			d := openTestDB(t)
			id := add(t, newCat(d), post(fmt.Sprintf("https://example.com/jobs/mv%d", i), "Role", "Co"))
			ts := newTestServer(t, d)
			if tc.from != "inbox" {
				now := time.Now().UTC().Format(time.RFC3339)
				d.Exec(`INSERT OR REPLACE INTO decision (profile,vacancy,stage,reason,decided_at) VALUES (1,?,?,?,?)`, id, tc.from, "", now) //nolint:errcheck
			}
			resp, status := apiJSON(t, ts, http.MethodPost, fmt.Sprintf("/api/vacancies/%d/decision", id),
				map[string]any{"profile": 1, "from": tc.from, "to": tc.to, "reason": tc.reason, "confirmed": tc.confirm})
			if status != http.StatusOK || resp["row"] == nil || resp["counts"] == nil {
				t.Errorf("move %s→%s: status %d, row %v, counts %v", tc.from, tc.to, status, resp["row"], resp["counts"])
			}
		})
	}
}

func TestDecision_Refusals(t *testing.T) {
	d := openTestDB(t)
	id := add(t, newCat(d), post("https://example.com/jobs/ref1", "Role", "Co"))
	ts := newTestServer(t, d)
	path := fmt.Sprintf("/api/vacancies/%d/decision", id)
	check := func(label string, wantStatus int, body map[string]any) {
		t.Helper()
		_, status := apiJSON(t, ts, http.MethodPost, path, body)
		if status != wantStatus {
			t.Errorf("%s: status %d, want %d", label, status, wantStatus)
		}
	}
	check("stale from", http.StatusConflict, map[string]any{"profile": 1, "from": "shortlist", "to": "inbox"})
	check("invalid pair", http.StatusUnprocessableEntity, map[string]any{"profile": 1, "from": "inbox", "to": "applied"})
	check("reason on inbox→dismissed", http.StatusUnprocessableEntity, map[string]any{"profile": 1, "from": "inbox", "to": "dismissed", "reason": "not a fit"})

	now := time.Now().UTC().Format(time.RFC3339)
	d.Exec(`INSERT OR REPLACE INTO decision (profile,vacancy,stage,reason,decided_at) VALUES (1,?,?,?,?)`, id, "shortlist", "", now) //nolint:errcheck
	check("apply without confirm", http.StatusUnprocessableEntity, map[string]any{"profile": 1, "from": "shortlist", "to": "applied", "confirmed": false})
	check("unknown profile", http.StatusNotFound, map[string]any{"profile": 999, "from": "inbox", "to": "shortlist"})
}

func TestDecision_AppliedWritesApplication_UndoKeepsIt(t *testing.T) {
	d := openTestDB(t)
	id := add(t, newCat(d), post("https://example.com/jobs/app1", "Role", "Co"))
	ts := newTestServer(t, d)
	now := time.Now().UTC().Format(time.RFC3339)
	d.Exec(`INSERT OR REPLACE INTO decision (profile,vacancy,stage,reason,decided_at) VALUES (1,?,?,?,?)`, id, "shortlist", "", now) //nolint:errcheck
	path := fmt.Sprintf("/api/vacancies/%d/decision", id)
	appCount := func() int {
		var n int
		d.QueryRow(`SELECT count(*) FROM application WHERE vacancy=?`, id).Scan(&n) //nolint:errcheck
		return n
	}
	_, status := apiJSON(t, ts, http.MethodPost, path, map[string]any{"profile": 1, "from": "shortlist", "to": "applied", "confirmed": true})
	if status != http.StatusOK || appCount() != 1 {
		t.Fatalf("apply: status %d, appCount %d", status, appCount())
	}
	_, status = apiJSON(t, ts, http.MethodPost, path, map[string]any{"profile": 1, "from": "applied", "to": "shortlist"})
	if status != http.StatusOK || appCount() != 1 {
		t.Errorf("undo: status %d, appCount %d (want 1 — application stays)", status, appCount())
	}
}

func TestDecision_AliasedVacancy(t *testing.T) {
	d := openTestDB(t)
	keptID := add(t, newCat(d), post("https://example.com/jobs/al", "Role", "Co"))
	d.Exec(`INSERT INTO vacancy_alias (old_id, new_id) VALUES (99999, ?)`, keptID) //nolint:errcheck
	ts := newTestServer(t, d)
	path := fmt.Sprintf("/api/vacancies/99999/decision")
	_, status := apiJSON(t, ts, http.MethodPost, path, map[string]any{"profile": 1, "from": "inbox", "to": "shortlist"})
	if status != http.StatusOK {
		t.Errorf("decision on alias: status %d", status)
	}
	var stage string
	d.QueryRow(`SELECT stage FROM decision WHERE vacancy=? AND profile=1`, keptID).Scan(&stage) //nolint:errcheck
	if stage != "shortlist" {
		t.Errorf("decision written to kept: stage %q", stage)
	}
}

// --- Seen endpoint ---

func TestSeen_MarksRead(t *testing.T) {
	d := openTestDB(t)
	id := add(t, newCat(d), post("https://example.com/jobs/seen1", "Role", "Co"))
	ts := newTestServer(t, d)
	_, status := apiJSON(t, ts, http.MethodPost, fmt.Sprintf("/api/vacancies/%d/seen", id), struct{}{})
	if status != http.StatusNoContent {
		t.Fatalf("seen: status %d, want 204", status)
	}
	var seenAt string
	d.QueryRow(`SELECT seen_at FROM vacancy WHERE id=?`, id).Scan(&seenAt) //nolint:errcheck
	if seenAt == "" {
		t.Error("seen_at: should be set after /seen")
	}
	apiJSON(t, ts, http.MethodPost, fmt.Sprintf("/api/vacancies/%d/seen", id), struct{}{}) //nolint:errcheck
	var seenAt2 string
	d.QueryRow(`SELECT seen_at FROM vacancy WHERE id=?`, id).Scan(&seenAt2) //nolint:errcheck
	if seenAt2 != seenAt {
		t.Errorf("seen_at changed on repeat call: %q → %q", seenAt, seenAt2)
	}
}

// --- Delete endpoint ---

func TestDelete_RemovesVacancy(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	id := add(t, cat, listing.Posting{
		URL:      "https://example.com/jobs/del1",
		ApplyURL: "https://jobs.lever.co/co/550e8400-e29b-41d4-a716-446655440000",
		Title:    "Role", Company: "Co",
		Text: "job", TextKind: listing.Plain, TextState: listing.TextFull,
	})
	ts := newTestServer(t, d)
	now := time.Now().UTC().Format(time.RFC3339)
	d.Exec(`INSERT OR REPLACE INTO decision (profile,vacancy,stage,reason,decided_at) VALUES (1,?,?,?,?)`, id, "shortlist", "", now) //nolint:errcheck
	_, status := apiJSON(t, ts, http.MethodDelete, fmt.Sprintf("/api/vacancies/%d?profile=1", id), nil)
	if status != http.StatusOK {
		t.Fatalf("delete: status %d, want 200", status)
	}
	var count, tkCount int
	d.QueryRow(`SELECT count(*) FROM vacancy WHERE id=?`, id).Scan(&count)             //nolint:errcheck
	d.QueryRow(`SELECT count(*) FROM tombstone WHERE key LIKE 'url:%'`).Scan(&tkCount) //nolint:errcheck
	if count != 0 {
		t.Errorf("vacancy still exists after delete")
	}
	if tkCount < 2 {
		t.Errorf("tombstone count: got %d, want >= 2", tkCount)
	}
	_, out, err := cat.Upsert(context.Background(),
		listing.Posting{URL: "https://example.com/jobs/del1", Title: "Role", Company: "Co", TextState: listing.TextNotFetched}, "src")
	if err != nil || out != catalog.OutcomeTombstoned {
		t.Errorf("re-upsert: outcome %v err %v, want Tombstoned", out, err)
	}
}

func TestDelete_Refused(t *testing.T) {
	d := openTestDB(t)
	id := add(t, newCat(d), post("https://example.com/jobs/drf1", "Role", "Co"))
	ts := newTestServer(t, d)

	// Refused in Inbox (no decision).
	_, status := apiJSON(t, ts, http.MethodDelete, fmt.Sprintf("/api/vacancies/%d?profile=1", id), nil)
	if status != http.StatusConflict {
		t.Errorf("delete in inbox: status %d, want 409", status)
	}

	// Refused while collect active.
	now := time.Now().UTC().Format(time.RFC3339)
	d.Exec(`INSERT OR REPLACE INTO decision (profile,vacancy,stage,reason,decided_at) VALUES (1,?,?,?,?)`, id, "shortlist", "", now) //nolint:errcheck
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := server.New(server.Options{Version: "test", Port: 9999, Logger: logger},
		func(mux *http.ServeMux) { inbox.RegisterRoutes(mux, d, testNowFn, logger, func() bool { return true }) })
	ts2 := httptest.NewServer(h)
	defer ts2.Close()
	_, status2 := apiJSON(t, ts2, http.MethodDelete, fmt.Sprintf("/api/vacancies/%d?profile=1", id), nil)
	if status2 != http.StatusLocked {
		t.Errorf("delete while collecting: status %d, want 423", status2)
	}
}
