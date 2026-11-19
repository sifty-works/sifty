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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sifty-works/sifty-server/internal/catalog"
	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/inbox"
	"github.com/sifty-works/sifty-server/internal/listing"
	"github.com/sifty-works/sifty-server/internal/server"
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
			inbox.RegisterRoutes(mux, d, testNowFn, logger, func() bool { return false }, nil, nil, nil)
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
	checkShape(t, http.MethodGet, path, body)
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
	if resp.StatusCode == http.StatusOK {
		checkShape(t, method, path, out)
	}
	return out, resp.StatusCode
}

// checkShape validates body against the registered response type for the
// given method and path. It is a no-op when no matching route is found.
func checkShape(t *testing.T, method, path string, body map[string]any) {
	t.Helper()
	base, _, _ := strings.Cut(path, "?")
	for _, ri := range server.Routes() {
		m, pat, _ := strings.Cut(ri.Pattern, " ")
		if m != method || ri.RespType == nil || !routeMatch(pat, base) {
			continue
		}
		for _, v := range server.ValidateShape(ri.RespType, body, "") {
			t.Errorf("response shape %s %s: %s", method, path, v)
		}
		return
	}
}

// routeMatch reports whether path matches pat. Segments enclosed in {} match
// any non-empty path segment; all others must be equal.
func routeMatch(pat, path string) bool {
	ps := strings.Split(strings.TrimPrefix(pat, "/"), "/")
	qs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(ps) != len(qs) {
		return false
	}
	for i, seg := range ps {
		if !strings.HasPrefix(seg, "{") && seg != qs[i] {
			return false
		}
	}
	return true
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
		Text: "job description", TextState: listing.TextFull,
	}
}

// setProfileData writes search fields to profile 1 so the inbox visibility filter
// selects the vacancies the test expects. Use invented terms only.
func setProfileData(t *testing.T, d *db.DB, roles, keywords, allOf []string) {
	t.Helper()
	type pd struct {
		Name     string   `json:"name"`
		Roles    []string `json:"targetRoles,omitempty"`
		Keywords []string `json:"keywords,omitempty"`
		AllOf    []string `json:"allOf,omitempty"`
	}
	b, _ := json.Marshal(pd{Name: "Profile", Roles: roles, Keywords: keywords, AllOf: allOf})
	if _, err := d.Exec(`UPDATE profile SET data=? WHERE id=1`, string(b)); err != nil {
		t.Fatalf("setProfileData: %v", err)
	}
}

// --- Rows and fields ---

func TestRows_AllFieldsPresent(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"description"}, nil)
	add(t, newCat(d), listing.Posting{
		URL: "https://example.com/jobs/1", Title: "Backend Engineer",
		Company: "Acme Corp", Location: "Remote · EU",
		Published: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), PublishedBasis: "datePosted",
		Text: "Full description.", TextState: listing.TextFull,
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
			setProfileData(t, d, nil, []string{"description"}, nil)
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
	setProfileData(t, d, nil, []string{"job"}, nil)
	cat := newCat(d)
	for _, p := range []listing.Posting{
		{URL: "https://example.com/jobs/ord1", Title: "Rank2", Company: "Co",
			Published: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC),
			Text:      "job", TextState: listing.TextFull},
		{URL: "https://example.com/jobs/ord2", Title: "Rank1 Newer", Company: "Co",
			Published: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC), PublishedBasis: "datePosted",
			Text: "job", TextState: listing.TextFull},
		{URL: "https://example.com/jobs/ord3", Title: "Rank3", Company: "Co",
			Text: "job", TextState: listing.TextFull},
		{URL: "https://example.com/jobs/ord4", Title: "Rank1 Older", Company: "Co",
			Published: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), PublishedBasis: "datePosted",
			Text: "job", TextState: listing.TextFull},
	} {
		add(t, cat, p)
	}
	rs := getRows(apiGet(t, newTestServer(t, d), "/api/vacancies?sort=newest"))
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
	setProfileData(t, d, nil, []string{"description"}, nil)
	t1 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	add(t, catalog.New(d, func() time.Time { return t1 }),
		post("https://example.com/jobs/tie1", "Tie Early", "Co"))
	id2 := add(t, catalog.New(d, func() time.Time { return t2 }),
		post("https://example.com/jobs/tie2", "Tie Later", "Co"))
	rs := getRows(apiGet(t, newTestServer(t, d), "/api/vacancies?sort=newest"))
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
	setProfileData(t, d, nil, []string{"backend", "designer", "engineer"}, nil)
	cat := newCat(d)
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/s1", Title: "Golang Backend", Company: "Acme Corp",
		Tags: []string{"Kubernetes"}, Text: "orchestration experience", TextState: listing.TextFull,
	})
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/s2", Title: "Frontend Designer", Company: "DeepMind AI",
		Tags: []string{"Figma"}, Text: "knowledge of color theory", TextState: listing.TextFull,
	})
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/s3", Title: "TypeScript Engineer", Company: "Acme",
		Text: "Must have OBSERVABILITY and distributed tracing systems", TextState: listing.TextFull,
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
	setProfileData(t, d, nil, []string{"job"}, nil)
	cat := newCat(d)
	add(t, cat, post("https://example.com/jobs/ex1", "Frontend Developer", "Globex"))
	add(t, cat, post("https://example.com/jobs/ex2", "Backend Engineer", "Acme Corp"))
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/ex3", Title: "Staff Engineer", Company: "Widget Co",
		ApplyURL: "https://boards.greenhouse.io/widgetboard/jobs/999",
		Text:     "job", TextState: listing.TextFull,
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
	setProfileData(t, d, nil, []string{"description"}, nil)
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
	setProfileData(t, d, nil, []string{"description"}, nil)
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
	setProfileData(t, d, nil, []string{"description"}, nil)
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
	setProfileData(t, d, nil, []string{"description"}, nil)
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
	setProfileData(t, d, nil, []string{"description"}, nil)
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
		Text: "job", TextState: listing.TextFull,
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
		func(mux *http.ServeMux) {
			inbox.RegisterRoutes(mux, d, testNowFn, logger, func() bool { return true }, nil, nil, nil)
		})
	ts2 := httptest.NewServer(h)
	defer ts2.Close()
	_, status2 := apiJSON(t, ts2, http.MethodDelete, fmt.Sprintf("/api/vacancies/%d?profile=1", id), nil)
	if status2 != http.StatusLocked {
		t.Errorf("delete while collecting: status %d, want 423", status2)
	}
}

// --- Vacancy view tests ---

// apiGetRaw performs a GET and returns the parsed body and status code.
func apiGetRaw(t *testing.T, ts *httptest.Server, path string) (map[string]any, int) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	req.Host = "127.0.0.1:9999"
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body) //nolint:errcheck
	return body, resp.StatusCode
}

// TestVacancyView_StageActionsAndStatus checks the actions and status text
// for each stage, and that an empty company has no block action.
func TestVacancyView_StageActionsAndStatus(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	nowStr := testNow.UTC().Format(time.RFC3339)
	setStage := func(id int64, stage, reason string) {
		d.Exec(`INSERT INTO decision (profile,vacancy,stage,reason,decided_at) VALUES (1,?,?,?,?)`, id, stage, reason, nowStr) //nolint:errcheck
	}
	inboxID := add(t, cat, post("https://example.com/jobs/vv1", "R", "Acme"))
	noCoID := add(t, cat, post("https://example.com/jobs/vv2", "R", ""))
	shortID := add(t, cat, post("https://example.com/jobs/vv3", "R", "Co"))
	setStage(shortID, "shortlist", "")
	applID := add(t, cat, post("https://example.com/jobs/vv4", "R", "Co"))
	setStage(applID, "applied", "")
	d.Exec(`INSERT INTO application (vacancy,applied_at,profile,profile_name) VALUES (?,?,1,'P')`, applID, "2026-11-01T12:00:00Z") //nolint:errcheck
	dismID := add(t, cat, post("https://example.com/jobs/vv5", "R", "Co"))
	setStage(dismID, "dismissed", "not a fit")
	ts := newTestServer(t, d)

	actionKeys := func(id int64) string {
		b, _ := apiGetRaw(t, ts, fmt.Sprintf("/api/vacancies/%d?profile=1", id))
		acts, _ := b["actions"].([]any)
		ks := make([]string, len(acts))
		for i, a := range acts {
			m, _ := a.(map[string]any)
			ks[i], _ = m["key"].(string)
		}
		return strings.Join(ks, ",")
	}
	status := func(id int64) string {
		b, _ := apiGetRaw(t, ts, fmt.Sprintf("/api/vacancies/%d?profile=1", id))
		v, _ := b["status"].(string)
		return v
	}

	for _, tc := range []struct {
		id      int64
		actions string
		status  string
	}{
		{inboxID, "block,dismiss,select", ""},
		{noCoID, "dismiss,select", ""},
		{shortID, "inbox,apply,confirm,delete", ""},
		{applID, "undo,delete", "Applied 1 Nov 2026"},
		{dismID, "restore,delete", "Dismissed — not a fit"},
	} {
		if got := actionKeys(tc.id); got != tc.actions {
			t.Errorf("actions id=%d: got %q, want %q", tc.id, got, tc.actions)
		}
		if got := status(tc.id); got != tc.status {
			t.Errorf("status id=%d: got %q, want %q", tc.id, got, tc.status)
		}
	}
}

// TestVacancyView_ExpiredVacancy checks that expired=true appears in both
// the view response and the list row when expires_at is in the past.
func TestVacancyView_ExpiredVacancy(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"description"}, nil)
	p := listing.Posting{
		URL: "https://example.com/jobs/exp1", Title: "R", Company: "Co",
		Text: "job description", TextState: listing.TextFull,
		Expires: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), // before testNow
	}
	id := add(t, newCat(d), p)
	ts := newTestServer(t, d)
	body, _ := apiGetRaw(t, ts, fmt.Sprintf("/api/vacancies/%d?profile=1", id))
	if exp, _ := body["expired"].(bool); !exp {
		t.Errorf("view: expired should be true")
	}
	for _, r := range getRows(apiGet(t, ts, "/api/vacancies")) {
		if rowID(r) == float64(id) {
			if exp, _ := r.(map[string]any)["expired"].(bool); !exp {
				t.Errorf("list row: expired should be true")
			}
		}
	}
}

// TestVacancyView_ApplyRules checks the apply action URL logic:
// valid apply_url is used; apply_url with credentials falls back to vacancy url;
// both urls invalid produces a message.
func TestVacancyView_ApplyRules(t *testing.T) {
	nowStr := testNow.UTC().Format(time.RFC3339)
	mkDB := func(url, applyURL string) (*db.DB, int64) {
		d := openTestDB(t)
		var id int64
		d.QueryRow(`INSERT INTO vacancy (identity,title,company,url,apply_url,first_seen,last_seen,text_state,content_hash,version,body)
			VALUES ('','R','C',?,?,?,?,'full','h',1,'') RETURNING id`,
			url, applyURL, nowStr, nowStr).Scan(&id) //nolint:errcheck
		d.Exec(`INSERT INTO vacancy_source (vacancy_id,source,url,seen_at) VALUES (?,'s',?,?)`, id, url, nowStr)           //nolint:errcheck
		d.Exec(`INSERT INTO decision (profile,vacancy,stage,reason,decided_at) VALUES (1,?,'shortlist','',?)`, id, nowStr) //nolint:errcheck
		return d, id
	}
	apply := func(d *db.DB, id int64) map[string]any {
		b, _ := apiGetRaw(t, newTestServer(t, d), fmt.Sprintf("/api/vacancies/%d?profile=1", id))
		for _, a := range b["actions"].([]any) {
			m, _ := a.(map[string]any)
			if k, _ := m["key"].(string); k == "apply" {
				return m
			}
		}
		return nil
	}
	d1, id1 := mkDB("https://ex.com/j1", "https://jobs.lever.co/co/abc")
	if u, _ := apply(d1, id1)["url"].(string); u != "https://jobs.lever.co/co/abc" {
		t.Errorf("valid apply_url: got %q", u)
	}
	d2, id2 := mkDB("https://ex.com/j2", "https://user:pass@bad.com/j")
	if u, _ := apply(d2, id2)["url"].(string); u != "https://ex.com/j2" {
		t.Errorf("apply_url with creds: fallback got %q", u)
	}
	d3, id3 := mkDB("ftp://nope/j3", "")
	if m, _ := apply(d3, id3)["message"].(string); m == "" {
		t.Errorf("both invalid: want message")
	}
}

// TestVacancyView_Sources checks that sources are returned as {name, url?} objects,
// using the configured name when available and the host otherwise, with url set for
// http(s) source keys and omitted for opaque identifiers.
func TestVacancyView_Sources(t *testing.T) {
	d := openTestDB(t)
	id := add(t, newCat(d), post("https://named.example.com/jobs/f1", "R", "Co"))
	nowStr := testNow.UTC().Format(time.RFC3339)
	d.Exec(`INSERT OR IGNORE INTO vacancy_source (vacancy_id,source,url,seen_at) VALUES (?,'https://hn.example.org','https://hn.example.org',?)`, id, nowStr) //nolint:errcheck
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := server.New(server.Options{Version: "test", Port: 9999, Logger: logger},
		func(mux *http.ServeMux) {
			inbox.RegisterRoutes(mux, d, testNowFn, logger, func() bool { return false },
				func() map[string]string { return map[string]string{"src": "Named Board"} }, nil, nil)
		})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	body, status := apiGetRaw(t, ts, fmt.Sprintf("/api/vacancies/%d?profile=1", id))
	if status != http.StatusOK {
		t.Fatalf("view: status %d", status)
	}
	srcs, _ := body["sources"].([]any)
	if len(srcs) != 2 {
		t.Fatalf("sources: got %d items, want 2", len(srcs))
	}
	// First source: opaque key "src" mapped to "Named Board"; no URL because "src" is not http(s).
	s0, _ := srcs[0].(map[string]any)
	if s0["name"] != "Named Board" {
		t.Errorf("named source name: got %q, want %q", s0["name"], "Named Board")
	}
	if _, hasURL := s0["url"]; hasURL {
		t.Errorf("named source: url should be absent for opaque key, got %q", s0["url"])
	}
	// Second source: "https://hn.example.org" → name from host, URL present.
	s1, _ := srcs[1].(map[string]any)
	if s1["name"] != "hn.example.org" {
		t.Errorf("unnamed source name: got %q, want %q", s1["name"], "hn.example.org")
	}
	if s1["url"] != "https://hn.example.org" {
		t.Errorf("unnamed source url: got %q, want %q", s1["url"], "https://hn.example.org")
	}
}

// TestVacancyView_SourceNameFromLiveConfig verifies that a source added to the
// configuration after the server starts is named on the vacancy view without
// restarting. The getSourceNames function is called on every request.
func TestVacancyView_SourceNameFromLiveConfig(t *testing.T) {
	d := openTestDB(t)
	// Source key "live-board" is not a URL, so without a name map it shows as "live-board".
	cat := catalog.New(d, testNowFn)
	id, _, err := cat.Upsert(context.Background(), post("https://live.example.com/jobs/1", "Staff Engineer", "LiveCo"), "live-board")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var mu sync.Mutex
	names := map[string]string{}
	h := server.New(server.Options{Version: "test", Port: 9999, Logger: logger},
		func(mux *http.ServeMux) {
			inbox.RegisterRoutes(mux, d, testNowFn, logger, func() bool { return false },
				func() map[string]string {
					mu.Lock()
					defer mu.Unlock()
					out := make(map[string]string, len(names))
					for k, v := range names {
						out[k] = v
					}
					return out
				}, nil, nil)
		})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	body1, _ := apiGetRaw(t, ts, fmt.Sprintf("/api/vacancies/%d?profile=1", id))
	srcs1, _ := body1["sources"].([]any)
	if len(srcs1) == 0 {
		t.Fatal("sources empty on first request")
	}
	s1, _ := srcs1[0].(map[string]any)
	if s1["name"] != "live-board" {
		t.Errorf("before config update: name %q, want %q", s1["name"], "live-board")
	}

	// Add the source to the live config; the next request must pick it up.
	mu.Lock()
	names["live-board"] = "Live Board"
	mu.Unlock()

	body2, _ := apiGetRaw(t, ts, fmt.Sprintf("/api/vacancies/%d?profile=1", id))
	srcs2, _ := body2["sources"].([]any)
	if len(srcs2) == 0 {
		t.Fatal("sources empty on second request")
	}
	s2, _ := srcs2[0].(map[string]any)
	if s2["name"] != "Live Board" {
		t.Errorf("after config update: name %q, want %q", s2["name"], "Live Board")
	}
}

// TestVacancyView_ListingState checks that each text_state value maps to its
// display string in the listing.state field.
func TestVacancyView_ListingState(t *testing.T) {
	for _, tc := range []struct {
		state listing.TextState
		want  string
	}{
		{listing.TextFull, "Full text"},
		{listing.TextSummary, "Listing summary only"},
		{listing.TextBlocked, "Blocked by site"},
		{listing.TextNotFetched, "Not fetched"},
		{listing.TextRemoved, "Text removed after 60 days unseen; it returns if the source shows the role again"},
	} {
		d := openTestDB(t)
		p := listing.Posting{
			URL: "https://example.com/jobs/ls-" + string(tc.state), Title: "R", Company: "Co",
			Text: "d", TextState: tc.state,
		}
		id := add(t, newCat(d), p)
		body, _ := apiGetRaw(t, newTestServer(t, d), fmt.Sprintf("/api/vacancies/%d?profile=1", id))
		lv, _ := body["listing"].(map[string]any)
		if s, _ := lv["state"].(string); s != tc.want {
			t.Errorf("state %s: got %q, want %q", tc.state, s, tc.want)
		}
	}
}

// TestVacancyView_AliasedVacancyAndNotFound checks that a merged-away id returns
// the kept vacancy, and that an unknown vacancy or profile answers 404.
func TestVacancyView_AliasedVacancyAndNotFound(t *testing.T) {
	d := openTestDB(t)
	keptID := add(t, newCat(d), post("https://example.com/jobs/vva1", "R", "Co"))
	d.Exec(`INSERT INTO vacancy_alias (old_id, new_id) VALUES (99997, ?)`, keptID) //nolint:errcheck
	ts := newTestServer(t, d)

	body, status := apiGetRaw(t, ts, "/api/vacancies/99997?profile=1")
	if status != http.StatusOK {
		t.Fatalf("alias: status %d", status)
	}
	if id, _ := body["id"].(float64); int64(id) != keptID {
		t.Errorf("alias: id %v, want %d", id, keptID)
	}
	_, status = apiGetRaw(t, ts, "/api/vacancies/99999?profile=1")
	if status != http.StatusNotFound {
		t.Errorf("unknown vacancy: status %d, want 404", status)
	}
	_, status = apiGetRaw(t, ts, fmt.Sprintf("/api/vacancies/%d?profile=99999", keptID))
	if status != http.StatusNotFound {
		t.Errorf("unknown profile: status %d, want 404", status)
	}
}

// --- Visibility ---

// TestVisibility_RolesOnly checks that target roles filter the inbox by title.
func TestVisibility_RolesOnly(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	setProfileData(t, d, []string{"engineer"}, nil, nil)
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/ro1", Title: "Senior Engineer", Company: "Acme",
		Text: "job description", TextState: listing.TextFull,
	})
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/ro2", Title: "Product Manager", Company: "Acme",
		Text: "job description", TextState: listing.TextFull,
	})
	rs := getRows(apiGet(t, newTestServer(t, d), "/api/vacancies"))
	if len(rs) != 1 {
		t.Fatalf("roles only: rows %d, want 1", len(rs))
	}
	if got := field(rs[0], "title"); got != "Senior Engineer" {
		t.Errorf("roles only: title %q, want Senior Engineer", got)
	}
}

// TestVisibility_KeywordsOnly checks that keywords filter the inbox by title, tags and text.
func TestVisibility_KeywordsOnly(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	setProfileData(t, d, nil, []string{"golang"}, nil)
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/kw1", Title: "Golang Backend", Company: "Acme",
		Text: "systems programming", TextState: listing.TextFull,
	})
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/kw2", Title: "Python Developer", Company: "Acme",
		Text: "data science work", TextState: listing.TextFull,
	})
	rs := getRows(apiGet(t, newTestServer(t, d), "/api/vacancies"))
	if len(rs) != 1 {
		t.Fatalf("keywords only: rows %d, want 1", len(rs))
	}
	if got := field(rs[0], "title"); got != "Golang Backend" {
		t.Errorf("keywords only: title %q, want Golang Backend", got)
	}
}

// TestVisibility_RolesAndKeywords checks that both role and keyword must match
// when the profile has both.
func TestVisibility_RolesAndKeywords(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	setProfileData(t, d, []string{"engineer"}, []string{"backend"}, nil)
	// Matches both: role "engineer" in title and keyword "backend" in title.
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/rk1", Title: "Backend Engineer", Company: "Acme",
		Text: "orchestration", TextState: listing.TextFull,
	})
	// Role matches, keyword does not appear anywhere.
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/rk2", Title: "Frontend Engineer", Company: "Acme",
		Text: "design systems", TextState: listing.TextFull,
	})
	// Keyword matches by title, role does not appear anywhere.
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/rk3", Title: "Backend Specialist", Company: "Acme",
		Text: "cloud services", TextState: listing.TextFull,
	})
	// Neither matches.
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/rk4", Title: "Product Manager", Company: "Acme",
		Text: "roadmap planning", TextState: listing.TextFull,
	})
	rs := getRows(apiGet(t, newTestServer(t, d), "/api/vacancies"))
	if len(rs) != 1 {
		t.Fatalf("roles+keywords: rows %d, want 1", len(rs))
	}
	if got := field(rs[0], "title"); got != "Backend Engineer" {
		t.Errorf("roles+keywords: title %q, want Backend Engineer", got)
	}
}

// TestVisibility_NeitherEmpty checks that a profile with no roles and no keywords
// sees an empty Inbox.
func TestVisibility_NeitherEmpty(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	add(t, cat, post("https://example.com/jobs/ne1", "Engineer", "Acme"))
	add(t, cat, post("https://example.com/jobs/ne2", "Developer", "Acme"))
	body := apiGet(t, newTestServer(t, d), "/api/vacancies")
	if rs := getRows(body); len(rs) != 0 {
		t.Errorf("neither roles nor keywords: rows %d, want 0", len(rs))
	}
	if countOf(body, "inbox") != 0 {
		t.Errorf("neither roles nor keywords: inbox count %v, want 0", countOf(body, "inbox"))
	}
}

// TestVisibility_AllOf checks that every "All of these" term must appear in a vacancy
// for it to be visible in the inbox.
func TestVisibility_AllOf(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	setProfileData(t, d, nil, []string{"senior"}, []string{"typescript"})
	// Keyword "senior" and allOf term "typescript" both present in title.
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/alof1", Title: "Senior TypeScript Developer", Company: "Acme",
		Text: "frontend work", TextState: listing.TextFull,
	})
	// Keyword matches but allOf term absent everywhere.
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/alof2", Title: "Senior Python Developer", Company: "Acme",
		Text: "data work", TextState: listing.TextFull,
	})
	// Neither keyword nor allOf term present.
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/alof3", Title: "Junior Developer", Company: "Acme",
		Text: "learning path", TextState: listing.TextFull,
	})
	rs := getRows(apiGet(t, newTestServer(t, d), "/api/vacancies"))
	if len(rs) != 1 {
		t.Fatalf("allOf: rows %d, want 1", len(rs))
	}
	if got := field(rs[0], "title"); got != "Senior TypeScript Developer" {
		t.Errorf("allOf: title %q, want Senior TypeScript Developer", got)
	}
}

// TestVisibility_ShortTerm checks that a term shorter than three characters
// matches as a whole word in the title, tags and text; it does not match a
// vacancy where the letters appear only inside a longer word.
func TestVisibility_ShortTerm(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	setProfileData(t, d, nil, []string{"ai"}, nil) // "ai" is 2 chars: whole-word match
	// "ai" appears in the title.
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/sh1", Title: "AI Researcher", Company: "Acme",
		Text: "language models", TextState: listing.TextFull,
	})
	// "ai" appears as a whole word in the body text.
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/sh2", Title: "Data Scientist", Company: "Acme",
		Text: "working with ai systems daily", TextState: listing.TextFull,
	})
	// "ai" appears only inside longer words; should not match.
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/sh3", Title: "Engineer", Company: "Acme",
		Text: "detail-oriented daily work", TextState: listing.TextFull,
	})
	rs := getRows(apiGet(t, newTestServer(t, d), "/api/vacancies"))
	if len(rs) != 2 {
		t.Fatalf("short term: rows %d, want 2", len(rs))
	}
	titles := map[string]bool{field(rs[0], "title"): true, field(rs[1], "title"): true}
	if !titles["AI Researcher"] || !titles["Data Scientist"] {
		t.Errorf("short term: titles %v, want AI Researcher and Data Scientist", titles)
	}
}

// TestVisibility_PerProfileExclusion checks that a word exclusion scoped to one profile
// hides a vacancy for that profile only.
func TestVisibility_PerProfileExclusion(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	setProfileData(t, d, nil, []string{"role"}, nil)
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/pe1", Title: "Restricted Role", Company: "Acme",
		Text: "some work", TextState: listing.TextFull,
	})
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/pe2", Title: "Open Role", Company: "Acme",
		Text: "other work", TextState: listing.TextFull,
	})
	// Add a word exclusion for profile 1 only (not all profiles).
	d.Exec(`INSERT INTO exclusion (kind,value,norm,profile_id,created) VALUES ('word','restricted','restricted',1,'2026-01-01T00:00:00Z')`) //nolint:errcheck

	rs := getRows(apiGet(t, newTestServer(t, d), "/api/vacancies"))
	if len(rs) != 1 {
		t.Fatalf("per-profile exclusion: rows %d, want 1", len(rs))
	}
	if got := field(rs[0], "title"); got != "Open Role" {
		t.Errorf("per-profile exclusion: title %q, want Open Role", got)
	}
}

// TestVisibility_DecidedVacancyStaysWhenSearchNoLongerFits checks that a vacancy
// in Shortlist remains there even after the profile search changes so the vacancy
// no longer matches, and that the inbox count is 0.
func TestVisibility_DecidedVacancyStaysWhenSearchNoLongerFits(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	setProfileData(t, d, nil, []string{"golang"}, nil)
	id1 := add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/dv1", Title: "Golang Developer", Company: "Acme",
		Text: "systems programming", TextState: listing.TextFull,
	})
	ts := newTestServer(t, d)
	now := testNow.UTC().Format(time.RFC3339)
	// Move to Shortlist while the vacancy fits the search.
	d.Exec(`INSERT INTO decision (profile,vacancy,stage,reason,decided_at) VALUES (1,?,?,?,?)`, //nolint:errcheck
		id1, "shortlist", "", now)

	// Change profile search so the vacancy no longer fits Inbox.
	setProfileData(t, d, nil, []string{"python"}, nil)

	// Shortlist still contains the vacancy.
	if rs := getRows(apiGet(t, ts, "/api/vacancies?stage=shortlist")); len(rs) != 1 {
		t.Errorf("shortlist after search change: rows %d, want 1", len(rs))
	}
	// Inbox is empty (vacancy is decided; no other vacancies match "python").
	body := apiGet(t, ts, "/api/vacancies?stage=inbox")
	if countOf(body, "inbox") != 0 {
		t.Errorf("inbox count after search change: %v, want 0", countOf(body, "inbox"))
	}
	if countOf(body, "shortlist") != 1 {
		t.Errorf("shortlist count after search change: %v, want 1", countOf(body, "shortlist"))
	}
}

// --- Whole-word FTS matching tests ---

// TestVisibility_WholeWordRole checks that a target role matches whole words
// only: "CTO" finds a vacancy titled "Interim CTO (remote)" but not one titled
// "Director of Sales".
func TestVisibility_WholeWordRole(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	setProfileData(t, d, []string{"CTO"}, nil, nil)
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/wwr1", Title: "Director of Sales", Company: "Acme",
		Text: "lead the sales team", TextState: listing.TextFull,
	})
	id2 := add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/wwr2", Title: "Interim CTO (remote)", Company: "Acme",
		Text: "lead the engineering team", TextState: listing.TextFull,
	})
	ts := newTestServer(t, d)
	rs := getRows(apiGet(t, ts, "/api/vacancies"))
	if len(rs) != 1 || rowID(rs[0]) != float64(id2) {
		t.Errorf("whole word role: rows %d, want 1 with id %d", len(rs), id2)
	}
}

// TestVisibility_ShortTermInText checks that a two-character keyword matches
// whole words in the text column; "JS" should find a vacancy whose text
// contains "JS" even though the term is shorter than three characters.
func TestVisibility_ShortTermInText(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	setProfileData(t, d, nil, []string{"JS"}, nil)
	id := add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/stt1", Title: "Frontend Developer", Company: "Acme",
		Text: "Experience with JS, React, and Vue", TextState: listing.TextFull,
	})
	ts := newTestServer(t, d)
	rs := getRows(apiGet(t, ts, "/api/vacancies"))
	if len(rs) != 1 || rowID(rs[0]) != float64(id) {
		t.Errorf("short term in text: rows %d, want 1 with id %d", len(rs), id)
	}
}

// TestVisibility_PhraseWithPunctuation checks that a keyword containing
// punctuation matches a vacancy where the same words appear without that
// punctuation: "Web3.js" matches a title "Web3 JS developer".
func TestVisibility_PhraseWithPunctuation(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	setProfileData(t, d, nil, []string{"Web3.js"}, nil)
	id := add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/pwp1", Title: "Web3 JS developer", Company: "Acme",
		TextState: listing.TextNotFetched,
	})
	ts := newTestServer(t, d)
	rs := getRows(apiGet(t, ts, "/api/vacancies"))
	if len(rs) != 1 || rowID(rs[0]) != float64(id) {
		t.Errorf("phrase with punctuation: rows %d, want 1 with id %d", len(rs), id)
	}
}

// TestSearch_NormalizationInText checks that the search query "node.js" is
// normalised to "node js" before the full-text search, so it finds a vacancy
// whose text contains "Node.js".
func TestSearch_NormalizationInText(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	setProfileData(t, d, nil, []string{"developer"}, nil)
	id := add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/sni1", Title: "Backend Developer", Company: "Acme",
		Text: "Using Node.js for server-side rendering", TextState: listing.TextFull,
	})
	ts := newTestServer(t, d)
	rs := getRows(apiGet(t, ts, "/api/vacancies?q=node.js"))
	if len(rs) != 1 || rowID(rs[0]) != float64(id) {
		t.Errorf("search normalisation: rows %d, want 1 with id %d", len(rs), id)
	}
}
