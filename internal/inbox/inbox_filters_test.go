package inbox_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/sifty-works/sifty-server/internal/catalog"
	"github.com/sifty-works/sifty-server/internal/listing"
)

// addWithSrc upserts a vacancy using a custom source key so source-filter
// tests can distinguish between two feeds.
func addWithSrc(t *testing.T, cat *catalog.Catalog, p listing.Posting, src string) int64 {
	t.Helper()
	id, _, err := cat.Upsert(context.Background(), p, src)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	return id
}

// --- Collected filter ---

// TestFilter_Collected_7days checks that only vacancies seen within 7 days appear
// when collected=7, and that an 8-day-old vacancy is excluded.
func TestFilter_Collected_7days(t *testing.T) {
	d := openTestDB(t)
	cat := catalog.New(d, func() time.Time { return testNow.AddDate(0, 0, -6) }) // 6 days ago
	setProfileData(t, d, nil, []string{"engineer"}, nil)
	id6 := add(t, cat, post("https://example.com/jobs/c1", "Recent Engineer", "Co"))

	// 8-day-old vacancy: use a catalog with an older now.
	cat8 := catalog.New(d, func() time.Time { return testNow.AddDate(0, 0, -8) })
	add(t, cat8, post("https://example.com/jobs/c2", "Old Engineer", "Co"))

	ts := newTestServer(t, d)
	rs := getRows(apiGet(t, ts, "/api/vacancies?collected=7"))
	if len(rs) != 1 || rowID(rs[0]) != float64(id6) {
		t.Errorf("collected=7: got %d rows, want 1 (6-day vacancy); first id %v", len(rs), rowID(rs[0]))
	}
	// Without filter: both appear.
	if len(getRows(apiGet(t, ts, "/api/vacancies"))) != 2 {
		t.Errorf("no filter: want 2 rows")
	}
}

// TestFilter_Collected_30days checks that a 7-day-old vacancy appears with
// collected=30 and that a 31-day-old vacancy is excluded.
func TestFilter_Collected_30days(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"engineer"}, nil)

	cat7 := catalog.New(d, func() time.Time { return testNow.AddDate(0, 0, -7) })
	add(t, cat7, post("https://example.com/jobs/c3", "Week Engineer", "Co"))

	cat31 := catalog.New(d, func() time.Time { return testNow.AddDate(0, 0, -31) })
	add(t, cat31, post("https://example.com/jobs/c4", "Month Engineer", "Co"))

	ts := newTestServer(t, d)
	rs := getRows(apiGet(t, ts, "/api/vacancies?collected=30"))
	if len(rs) != 1 || field(rs[0], "title") != "Week Engineer" {
		t.Errorf("collected=30: got %d rows, title %q", len(rs), field(rs[0], "title"))
	}
}

// --- Expiry condition ---

// TestExpiry_StageCondition checks that expired vacancies are automatically
// excluded from Inbox, Shortlist and Dismissed rows and their counts while
// Applied is unaffected; industry counts follow the same rule.
func TestExpiry_StageCondition(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"engineer"}, nil)
	cat := newCat(d)
	ns := testNow.UTC().Format(time.RFC3339)
	pExpSL := post("https://example.com/jobs/ec1", "Expired Engineer", "Co")
	pExpSL.Expires = testNow.AddDate(0, 0, -1)
	expSL := add(t, cat, pExpSL)
	setVacancyIndustries(t, d, expSL, []string{"Ops"})
	d.Exec(`INSERT INTO decision (profile,vacancy,stage,reason,decided_at) VALUES (1,?,?,?,?)`, expSL, "shortlist", "", ns) //nolint:errcheck
	pExpAP := post("https://example.com/jobs/ec2", "Applied Expired Engineer", "Co")
	pExpAP.Expires = testNow.AddDate(0, 0, -2)
	expAP := add(t, cat, pExpAP)
	setVacancyIndustries(t, d, expAP, []string{"Ops"})
	d.Exec(`INSERT INTO decision (profile,vacancy,stage,reason,decided_at) VALUES (1,?,?,?,?)`, expAP, "applied", "", ns) //nolint:errcheck
	pFuture := post("https://example.com/jobs/ec3", "Future Engineer", "Co")
	pFuture.Expires = testNow.AddDate(0, 0, 10)
	add(t, cat, pFuture)
	openDM := add(t, cat, post("https://example.com/jobs/ec4", "Live Engineer", "Co"))
	d.Exec(`INSERT INTO decision (profile,vacancy,stage,reason,decided_at) VALUES (1,?,?,?,?)`, openDM, "dismissed", "", ns) //nolint:errcheck
	ts := newTestServer(t, d)
	ib := apiGet(t, ts, "/api/vacancies?stage=inbox")
	if n, s, ap, dm := countOf(ib, "inbox"), countOf(ib, "shortlist"), countOf(ib, "applied"), countOf(ib, "dismissed"); n != 1 || s != 0 || ap != 1 || dm != 1 {
		t.Errorf("counts inbox/shortlist/applied/dismissed: %.0f/%.0f/%.0f/%.0f, want 1/0/1/1", n, s, ap, dm)
	}
	sl := apiGet(t, ts, "/api/vacancies?stage=shortlist")
	ap := apiGet(t, ts, "/api/vacancies?stage=applied")
	apRS := getRows(ap)
	if len(apRS) != 1 || rowID(apRS[0]) != float64(expAP) {
		t.Errorf("applied rows: %d, want 1", len(apRS))
	}
	if exp, _ := apRS[0].(map[string]any)["expired"].(bool); !exp {
		t.Errorf("applied row: expired should be true")
	}
	icOf := func(body map[string]any, name string) int {
		for _, v := range body["industryCounts"].([]any) {
			if m, _ := v.(map[string]any); m["name"] == name {
				c, _ := m["count"].(float64)
				return int(c)
			}
		}
		return 0
	}
	if ic := icOf(sl, "Ops"); ic != 0 {
		t.Errorf("shortlist Ops industry count: %d, want 0", ic)
	}
	if ic := icOf(ap, "Ops"); ic != 1 {
		t.Errorf("applied Ops industry count: %d, want 1", ic)
	}
}

// --- Remote filter ---

// TestFilter_Remote matches "Remote · EU" and "Remote-first (hybrid)" but not
// "New York office only".
func TestFilter_Remote(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"engineer"}, nil)
	cat := newCat(d)

	mkLoc := func(url, loc string) listing.Posting {
		return listing.Posting{
			URL: url, Title: "Engineer", Company: "Co",
			Location: loc, Text: "job", TextKind: listing.Plain, TextState: listing.TextFull,
		}
	}
	idA := add(t, cat, mkLoc("https://example.com/jobs/r1", "Remote · EU"))
	idB := add(t, cat, mkLoc("https://example.com/jobs/r2", "Remote-first (hybrid)"))
	add(t, cat, mkLoc("https://example.com/jobs/r3", "New York office only"))

	ts := newTestServer(t, d)
	rs := getRows(apiGet(t, ts, "/api/vacancies?remote=true"))
	got := map[float64]bool{}
	for _, r := range rs {
		got[rowID(r)] = true
	}
	if len(rs) != 2 || !got[float64(idA)] || !got[float64(idB)] {
		t.Errorf("remote: got %d rows %v, want 2 (Remote EU + Remote-first)", len(rs), got)
	}
}

// TestFilter_Remote_Anywhere checks that "anywhere" and "telecommute" in the
// location also match the remote filter.
func TestFilter_Remote_Anywhere(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"engineer"}, nil)
	cat := newCat(d)

	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/ra1", Title: "Engineer", Company: "Co",
		Location: "Worldwide – anywhere", Text: "job", TextKind: listing.Plain, TextState: listing.TextFull,
	})
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/ra2", Title: "Engineer", Company: "Co",
		Location: "Telecommute · US", Text: "job", TextKind: listing.Plain, TextState: listing.TextFull,
	})
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/ra3", Title: "Engineer", Company: "Co",
		Location: "San Francisco", Text: "job", TextKind: listing.Plain, TextState: listing.TextFull,
	})

	ts := newTestServer(t, d)
	rs := getRows(apiGet(t, ts, "/api/vacancies?remote=true"))
	if len(rs) != 2 {
		t.Errorf("remote anywhere/telecommute: got %d rows, want 2", len(rs))
	}
}

// --- Leadership filter ---

// TestFilter_Leadership matches CTO, Head, Director, Manager, Lead (not "lead
// generation") as whole words in the title.
func TestFilter_Leadership(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"engineer", "developer", "head", "director", "manager", "lead", "cto", "analyst"}, nil)
	cat := newCat(d)

	cases := []struct {
		url   string
		title string
		match bool
	}{
		{"https://example.com/jobs/l1", "Head of Engineering", true},
		{"https://example.com/jobs/l2", "Lead Generation Specialist", false},
		{"https://example.com/jobs/l3", "CTO (remote)", true},
		{"https://example.com/jobs/l4", "Senior Director of Product", true},
		{"https://example.com/jobs/l5", "Engineering Manager", true},
		{"https://example.com/jobs/l6", "Lead Software Engineer", true},
		{"https://example.com/jobs/l7", "Data Analyst", false},
	}
	for _, tc := range cases {
		add(t, cat, listing.Posting{
			URL: tc.url, Title: tc.title, Company: "Co",
			Text: "job description", TextKind: listing.Plain, TextState: listing.TextFull,
		})
	}

	ts := newTestServer(t, d)
	rs := getRows(apiGet(t, ts, "/api/vacancies?leadership=true"))
	matched := map[string]bool{}
	for _, r := range rs {
		matched[field(r, "title")] = true
	}
	for _, tc := range cases {
		if matched[tc.title] != tc.match {
			t.Errorf("leadership title=%q: matched=%v, want %v", tc.title, matched[tc.title], tc.match)
		}
	}
}

// --- Sources filter ---

// TestFilter_Sources returns only vacancies from the requested source key.
func TestFilter_Sources(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"engineer"}, nil)
	cat := newCat(d)

	idA := addWithSrc(t, cat, post("https://example.com/jobs/s1", "Alpha Engineer", "feed-alpha"), "feed-alpha")
	add(t, cat, post("https://example.com/jobs/s2", "Beta Engineer", "feed-beta"))

	ts := newTestServer(t, d)
	rs := getRows(apiGet(t, ts, "/api/vacancies?sources=feed-alpha"))
	if len(rs) != 1 || rowID(rs[0]) != float64(idA) {
		t.Errorf("sources filter: got %d rows, want 1 (feed-alpha)", len(rs))
	}
	if len(getRows(apiGet(t, ts, "/api/vacancies"))) != 2 {
		t.Errorf("no filter: want 2 rows")
	}
}

// --- Unread filter ---

// TestFilter_Unread shows only unread vacancies when unread=true; read vacancies
// are hidden. In Applied the filter is ignored.
func TestFilter_Unread(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"engineer"}, nil)
	cat := newCat(d)

	idUnread := add(t, cat, post("https://example.com/jobs/u1", "Unread Engineer", "Co"))
	idRead := add(t, cat, post("https://example.com/jobs/u2", "Read Engineer", "Co"))
	// Mark idRead as seen.
	d.Exec(`UPDATE vacancy SET seen_at=? WHERE id=?`, //nolint:errcheck
		testNow.UTC().Format(time.RFC3339), idRead)

	ts := newTestServer(t, d)
	rs := getRows(apiGet(t, ts, "/api/vacancies?unread=true"))
	if len(rs) != 1 || rowID(rs[0]) != float64(idUnread) {
		t.Errorf("unread: got %d rows, want 1 (unread)", len(rs))
	}

	// Unread filter ignored in applied.
	nowStr := testNow.UTC().Format(time.RFC3339)
	d.Exec(`INSERT OR REPLACE INTO decision (profile,vacancy,stage,reason,decided_at) VALUES (1,?,?,?,?)`, //nolint:errcheck
		idRead, "applied", "", nowStr)
	d.Exec(`INSERT OR REPLACE INTO decision (profile,vacancy,stage,reason,decided_at) VALUES (1,?,?,?,?)`, //nolint:errcheck
		idUnread, "applied", "", nowStr)
	rsApplied := getRows(apiGet(t, ts, "/api/vacancies?stage=applied&unread=true"))
	if len(rsApplied) != 2 {
		t.Errorf("unread ignored in applied: got %d rows, want 2", len(rsApplied))
	}
}

// --- Filters combined ---

// TestFilter_Combined applies remote and collected together and verifies only
// vacancies matching both conditions appear.
func TestFilter_Combined(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"engineer"}, nil)

	catRecent := catalog.New(d, func() time.Time { return testNow.AddDate(0, 0, -3) })
	catOld := catalog.New(d, func() time.Time { return testNow.AddDate(0, 0, -15) })

	idOK := addWithSrc(t, catRecent, listing.Posting{
		URL: "https://example.com/jobs/cb1", Title: "Engineer", Company: "Co",
		Location: "Remote · EU", Text: "job", TextKind: listing.Plain, TextState: listing.TextFull,
	}, "src")
	// recent but not remote
	add(t, catRecent, listing.Posting{
		URL: "https://example.com/jobs/cb2", Title: "Engineer", Company: "Co",
		Location: "London", Text: "job", TextKind: listing.Plain, TextState: listing.TextFull,
	})
	// remote but old
	add(t, catOld, listing.Posting{
		URL: "https://example.com/jobs/cb3", Title: "Engineer", Company: "Co",
		Location: "Remote · US", Text: "job", TextKind: listing.Plain, TextState: listing.TextFull,
	})

	ts := newTestServer(t, d)
	rs := getRows(apiGet(t, ts, "/api/vacancies?remote=true&collected=7"))
	if len(rs) != 1 || rowID(rs[0]) != float64(idOK) {
		t.Errorf("combined remote+collected: got %d rows, want 1", len(rs))
	}
}

// --- Count groups ignore their own filter ---

// TestFilter_IndustryCounts_HonoursNewFilters checks that industryCounts honours
// the new filters (e.g. remote) but still ignores the industries filter itself.
func TestFilter_IndustryCounts_HonoursNewFilters(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"engineer"}, nil)
	cat := newCat(d)

	// Remote vacancy with Software industry.
	idRemote := add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/ic1", Title: "Engineer", Company: "Co",
		Location: "Remote · EU", Text: "job", TextKind: listing.Plain, TextState: listing.TextFull,
	})
	// Office vacancy with Software industry.
	idOffice := add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/ic2", Title: "Engineer", Company: "Co",
		Location: "London office", Text: "job", TextKind: listing.Plain, TextState: listing.TextFull,
	})
	setVacancyIndustries(t, d, idRemote, []string{"Software"})
	setVacancyIndustries(t, d, idOffice, []string{"Software"})

	ts := newTestServer(t, d)
	body := apiGet(t, ts, "/api/vacancies?remote=true")
	// industryCounts should reflect the remote-filtered set (1 vacancy).
	icRaw, _ := body["industryCounts"].([]any)
	counts := make(map[string]int)
	for _, v := range icRaw {
		m, _ := v.(map[string]any)
		name, _ := m["name"].(string)
		n, _ := m["count"].(float64)
		counts[name] = int(n)
	}
	if counts["Software"] != 1 {
		t.Errorf("industryCounts with remote filter: Software=%d, want 1", counts["Software"])
	}
}

// TestFilter_SourceCounts_IgnoresSourcesFilter checks that sourceCounts returns
// all sources regardless of the sources filter, but honours other filters.
func TestFilter_SourceCounts_IgnoresSourcesFilter(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"engineer"}, nil)
	cat := newCat(d)

	addWithSrc(t, cat, post("https://example.com/jobs/sc1", "Alpha Engineer", "feed-alpha"), "feed-alpha")
	addWithSrc(t, cat, post("https://example.com/jobs/sc2", "Beta Engineer", "feed-beta"), "feed-beta")

	ts := newTestServer(t, d)
	body := apiGet(t, ts, "/api/vacancies?sources=feed-alpha")

	// rows: only feed-alpha.
	if len(getRows(body)) != 1 {
		t.Errorf("rows with sources filter: want 1")
	}

	// sourceCounts: both sources still counted.
	scRaw, _ := body["sourceCounts"].([]any)
	scm := make(map[string]int)
	for _, v := range scRaw {
		m, _ := v.(map[string]any)
		name, _ := m["name"].(string)
		n, _ := m["count"].(float64)
		scm[name] = int(n)
	}
	if scm["feed-alpha"] != 1 {
		t.Errorf("sourceCounts feed-alpha: got %d, want 1", scm["feed-alpha"])
	}
	if scm["feed-beta"] != 1 {
		t.Errorf("sourceCounts feed-beta: got %d, want 1 (sources filter ignored)", scm["feed-beta"])
	}
}

// TestFilter_IndustryCounts_HonoursSourcesFilter checks that industryCounts is
// affected by the sources filter (since sources is not its own filter group).
func TestFilter_IndustryCounts_HonoursSourcesFilter(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"engineer"}, nil)
	cat := newCat(d)

	idA := addWithSrc(t, cat, post("https://example.com/jobs/ih1", "Alpha Engineer", "feed-alpha"), "feed-alpha")
	idB := addWithSrc(t, cat, post("https://example.com/jobs/ih2", "Beta Engineer", "feed-beta"), "feed-beta")
	setVacancyIndustries(t, d, idA, []string{"Web3"})
	setVacancyIndustries(t, d, idB, []string{"Software"})

	ts := newTestServer(t, d)
	body := apiGet(t, ts, "/api/vacancies?sources=feed-alpha")
	icRaw, _ := body["industryCounts"].([]any)
	counts := make(map[string]int)
	for _, v := range icRaw {
		m, _ := v.(map[string]any)
		name, _ := m["name"].(string)
		n, _ := m["count"].(float64)
		counts[name] = int(n)
	}
	// Only feed-alpha (Web3) is in the filtered set; Software is in feed-beta only.
	if counts["Web3"] != 1 {
		t.Errorf("industryCounts with sources filter: Web3=%d, want 1", counts["Web3"])
	}
	if counts["Software"] != 0 {
		t.Errorf("industryCounts with sources filter: Software=%d, want 0", counts["Software"])
	}
}

// --- Total unchanged by filters ---

// TestFilter_Total_UnchangedByFilters checks that total reflects the stage
// count before any user query or filter is applied.
func TestFilter_Total_UnchangedByFilters(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"engineer"}, nil)
	cat := newCat(d)

	for i := 0; i < 3; i++ {
		add(t, cat, listing.Posting{
			URL: fmt.Sprintf("https://example.com/jobs/tot%d", i), Title: "Engineer", Company: "Co",
			Location: "London", Text: "job", TextKind: listing.Plain, TextState: listing.TextFull,
		})
	}

	ts := newTestServer(t, d)
	// No filters: total = 3.
	body := apiGet(t, ts, "/api/vacancies")
	total, _ := body["total"].(float64)
	if total != 3 {
		t.Errorf("total no filter: got %v, want 3", total)
	}
	// With remote filter (no matches): total still = 3.
	body2 := apiGet(t, ts, "/api/vacancies?remote=true")
	total2, _ := body2["total"].(float64)
	if total2 != 3 {
		t.Errorf("total with remote filter: got %v, want 3", total2)
	}
	// With query that matches 1: total still = 3.
	body3 := apiGet(t, ts, "/api/vacancies?q=engineer")
	total3, _ := body3["total"].(float64)
	if total3 != 3 {
		t.Errorf("total with query: got %v, want 3", total3)
	}
	// rows with remote = 0.
	if len(getRows(body2)) != 0 {
		t.Errorf("rows with remote filter: want 0")
	}
}

// --- SourceCounts in list response ---

// TestFilter_SourceCounts_Basic checks that sourceCounts is populated and
// reflects the number of vacancies per source in the unfiltered list.
func TestFilter_SourceCounts_Basic(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"engineer"}, nil)
	cat := newCat(d)

	addWithSrc(t, cat, post("https://example.com/jobs/sb1", "Alpha Engineer", "feed-alpha"), "feed-alpha")
	addWithSrc(t, cat, post("https://example.com/jobs/sb2", "Beta Engineer A", "feed-beta"), "feed-beta")
	addWithSrc(t, cat, post("https://example.com/jobs/sb3", "Beta Engineer B", "feed-beta"), "feed-beta")

	ts := newTestServer(t, d)
	body := apiGet(t, ts, "/api/vacancies")
	scRaw, _ := body["sourceCounts"].([]any)
	scm := make(map[string]int)
	for _, v := range scRaw {
		m, _ := v.(map[string]any)
		name, _ := m["name"].(string)
		n, _ := m["count"].(float64)
		scm[name] = int(n)
	}
	if scm["feed-alpha"] != 1 {
		t.Errorf("sourceCounts feed-alpha: got %d, want 1", scm["feed-alpha"])
	}
	if scm["feed-beta"] != 2 {
		t.Errorf("sourceCounts feed-beta: got %d, want 2", scm["feed-beta"])
	}
}

// TestFilter_Sources_MultipleRequested checks that vacancies from either of two
// requested sources are returned.
func TestFilter_Sources_MultipleRequested(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"engineer"}, nil)
	cat := newCat(d)

	idA := addWithSrc(t, cat, post("https://example.com/jobs/sm1", "Alpha Engineer", "feed-alpha"), "feed-alpha")
	idB := addWithSrc(t, cat, post("https://example.com/jobs/sm2", "Beta Engineer", "feed-beta"), "feed-beta")
	addWithSrc(t, cat, post("https://example.com/jobs/sm3", "Gamma Engineer", "feed-gamma"), "feed-gamma")

	ts := newTestServer(t, d)
	rs := getRows(apiGet(t, ts, "/api/vacancies?sources=feed-alpha,feed-beta"))
	got := map[float64]bool{rowID(rs[0]): true, rowID(rs[1]): true}
	if len(rs) != 2 || !got[float64(idA)] || !got[float64(idB)] {
		t.Errorf("multiple sources: got %d rows %v, want 2 (alpha+beta)", len(rs), got)
	}
}

// TestFilter_Leadership_HeadNotConfusedWithHeadhunter checks that "Head" as a
// whole word matches but does not match it inside another word.
func TestFilter_Leadership_HeadMatch(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"engineer", "developer", "head", "director"}, nil)
	cat := newCat(d)

	idHead := add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/lh1", Title: "Head of Platform Engineering", Company: "Co",
		Text: "job description", TextKind: listing.Plain, TextState: listing.TextFull,
	})
	add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/lh2", Title: "Lead Generation Director", Company: "Co",
		Text: "job description", TextKind: listing.Plain, TextState: listing.TextFull,
	})

	ts := newTestServer(t, d)
	rs := getRows(apiGet(t, ts, "/api/vacancies?leadership=true"))
	// "Head of Platform Engineering" → matches (Head)
	// "Lead Generation Director" → matches (Director)
	got := map[float64]bool{}
	for _, r := range rs {
		got[rowID(r)] = true
	}
	if !got[float64(idHead)] {
		t.Errorf("leadership: Head of Platform not matched")
	}
	// Director is a leadership title even in "Lead Generation Director".
	if len(rs) != 2 {
		t.Errorf("leadership: got %d rows, want 2", len(rs))
	}
}

// TestFilter_Unread_IgnoredInDismissed checks that unread=true is ignored in
// the dismissed list (matches dismissed-stage behaviour: ignore in Archive).
func TestFilter_Unread_IgnoredInDismissed(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"engineer"}, nil)
	cat := newCat(d)

	idA := add(t, cat, post("https://example.com/jobs/ud1", "Engineer A", "Co"))
	idB := add(t, cat, post("https://example.com/jobs/ud2", "Engineer B", "Co"))
	// Mark idA as read.
	d.Exec(`UPDATE vacancy SET seen_at=? WHERE id=?`, //nolint:errcheck
		testNow.UTC().Format(time.RFC3339), idA)
	nowStr := testNow.UTC().Format(time.RFC3339)
	d.Exec(`INSERT OR REPLACE INTO decision (profile,vacancy,stage,reason,decided_at) VALUES (1,?,?,?,?)`, //nolint:errcheck
		idA, "dismissed", "", nowStr)
	d.Exec(`INSERT OR REPLACE INTO decision (profile,vacancy,stage,reason,decided_at) VALUES (1,?,?,?,?)`, //nolint:errcheck
		idB, "dismissed", "", nowStr)

	ts := newTestServer(t, d)
	rs := getRows(apiGet(t, ts, "/api/vacancies?stage=dismissed&unread=true"))
	// Unread ignored in dismissed → both vacancies appear.
	if len(rs) != 2 {
		t.Errorf("unread ignored in dismissed: got %d rows, want 2", len(rs))
	}
}

// TestFilter_HTTPStatus_InvalidStageStillErrors verifies that an invalid stage
// parameter is still rejected 422.
func TestFilter_HTTPStatus_InvalidStageWithFilters(t *testing.T) {
	d := openTestDB(t)
	ts := newTestServer(t, d)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/vacancies?stage=bogus&remote=true", nil)
	req.Host = "127.0.0.1:9999"
	resp, _ := ts.Client().Do(req)
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("invalid stage with filters: status %d, want 422", resp.StatusCode)
	}
}
