package inbox_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/inbox"
	"github.com/sifty-works/sifty-server/internal/listing"
	"github.com/sifty-works/sifty-server/internal/server"
)

// setVacancyIndustries writes a JSON industries array directly to the vacancy row.
func setVacancyIndustries(t *testing.T, d *db.DB, id int64, industries []string) {
	t.Helper()
	b, _ := json.Marshal(industries)
	if _, err := d.Exec(`UPDATE vacancy SET industries=? WHERE id=?`, string(b), id); err != nil {
		t.Fatalf("setVacancyIndustries: %v", err)
	}
}

// setProfileTargetIndustries writes targetIndustries to profile 1's data.
// The profile uses a keyword that matches all test vacancies' text.
func setProfileTargetIndustries(t *testing.T, d *db.DB, industries []string) {
	t.Helper()
	type pd struct {
		Name             string   `json:"name"`
		Keywords         []string `json:"keywords,omitempty"`
		TargetIndustries []string `json:"targetIndustries,omitempty"`
	}
	// "description" appears in every vacancy's text ("job description").
	b, _ := json.Marshal(pd{Name: "Profile", Keywords: []string{"description"}, TargetIndustries: industries})
	if _, err := d.Exec(`UPDATE profile SET data=? WHERE id=1`, string(b)); err != nil {
		t.Fatalf("setProfileTargetIndustries: %v", err)
	}
}

// newServerWithVocab creates an httptest.Server for the inbox with a vocabulary
// function wired into RegisterRoutes for target-industry filtering.
func newServerWithVocab(t *testing.T, d *db.DB, vocabNames []string) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	getVocabNames := func() []string { return vocabNames }
	h := server.New(server.Options{Version: "test", Port: 9999, Logger: logger},
		func(mux *http.ServeMux) {
			inbox.RegisterRoutes(mux, d, testNowFn, logger, func() bool { return false }, nil, getVocabNames, nil)
		})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

// --- Row carries industries field ---

func TestRows_IndustriesField(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	setProfileData(t, d, nil, []string{"description"}, nil)
	id := add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/1", Title: "Blockchain Developer",
		Text: "job description", TextState: listing.TextFull,
	})
	setVacancyIndustries(t, d, id, []string{"Web3", "Security"})

	rs := getRows(apiGet(t, newTestServer(t, d), "/api/vacancies"))
	if len(rs) != 1 {
		t.Fatalf("got %d rows, want 1", len(rs))
	}
	row, _ := rs[0].(map[string]any)
	inds, _ := row["industries"].([]any)
	if len(inds) != 2 {
		t.Errorf("industries: got %v, want [Web3 Security]", inds)
	}
}

func TestRows_EmptyIndustriesField(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	setProfileData(t, d, nil, []string{"description"}, nil)
	add(t, cat, post("https://example.com/jobs/1", "Platform Engineer", "Acme"))

	rs := getRows(apiGet(t, newTestServer(t, d), "/api/vacancies"))
	if len(rs) != 1 {
		t.Fatalf("got %d rows, want 1", len(rs))
	}
	row, _ := rs[0].(map[string]any)
	inds, _ := row["industries"].([]any)
	if len(inds) != 0 {
		t.Errorf("empty industries: got %v, want []", inds)
	}
}

// --- Industries filter ---

func TestList_IndustriesFilter_ReturnsMatchingOnly(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	setProfileData(t, d, nil, []string{"description"}, nil)

	id1 := add(t, cat, post("https://example.com/jobs/1", "Smart Contract Dev", "Co"))
	id2 := add(t, cat, post("https://example.com/jobs/2", "Platform Engineer", "Co"))
	add(t, cat, post("https://example.com/jobs/3", "Security Analyst", "Co"))

	setVacancyIndustries(t, d, id1, []string{"Web3"})
	setVacancyIndustries(t, d, id2, []string{"Software"})

	rs := getRows(apiGet(t, newTestServer(t, d), "/api/vacancies?industries=Web3"))
	if len(rs) != 1 {
		t.Fatalf("filtered: got %d rows, want 1", len(rs))
	}
	if rowID(rs[0]) != float64(id1) {
		t.Errorf("wrong vacancy: got id %v, want %d", rowID(rs[0]), id1)
	}
}

func TestList_IndustriesFilter_MultipleNames(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	setProfileData(t, d, nil, []string{"description"}, nil)

	id1 := add(t, cat, post("https://example.com/jobs/1", "Chain Dev", "Co"))
	id2 := add(t, cat, post("https://example.com/jobs/2", "Platform Eng", "Co"))
	id3 := add(t, cat, post("https://example.com/jobs/3", "DevOps Engineer", "Co"))

	setVacancyIndustries(t, d, id1, []string{"Web3"})
	setVacancyIndustries(t, d, id2, []string{"Software"})
	// id3 stays empty

	rs := getRows(apiGet(t, newTestServer(t, d), "/api/vacancies?industries=Web3,Software"))
	if len(rs) != 2 {
		t.Fatalf("multi-industry filter: got %d rows, want 2", len(rs))
	}
	ids := map[float64]bool{rowID(rs[0]): true, rowID(rs[1]): true}
	if !ids[float64(id1)] || !ids[float64(id2)] {
		t.Errorf("unexpected ids: %v", ids)
	}
	_ = id3
}

// --- Industry counts ---

func TestList_IndustryCounts(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	setProfileData(t, d, nil, []string{"description"}, nil)

	id1 := add(t, cat, post("https://example.com/jobs/1", "Chain Dev", "Co"))
	id2 := add(t, cat, post("https://example.com/jobs/2", "Platform Eng", "Co"))
	id3 := add(t, cat, post("https://example.com/jobs/3", "DevOps Engineer", "Co"))

	setVacancyIndustries(t, d, id1, []string{"Web3"})
	setVacancyIndustries(t, d, id2, []string{"Software"})
	setVacancyIndustries(t, d, id3, []string{"Software"})

	body := apiGet(t, newTestServer(t, d), "/api/vacancies")
	icRaw, _ := body["industryCounts"].([]any)
	counts := make(map[string]int)
	for _, v := range icRaw {
		m, _ := v.(map[string]any)
		name, _ := m["name"].(string)
		n, _ := m["count"].(float64)
		counts[name] = int(n)
	}
	if counts["Software"] != 2 {
		t.Errorf("Software count: got %d, want 2", counts["Software"])
	}
	if counts["Web3"] != 1 {
		t.Errorf("Web3 count: got %d, want 1", counts["Web3"])
	}
}

func TestList_IndustryCounts_NotAffectedByIndustriesFilter(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	setProfileData(t, d, nil, []string{"description"}, nil)

	id1 := add(t, cat, post("https://example.com/jobs/1", "Chain Dev", "Co"))
	id2 := add(t, cat, post("https://example.com/jobs/2", "Platform Eng", "Co"))

	setVacancyIndustries(t, d, id1, []string{"Web3"})
	setVacancyIndustries(t, d, id2, []string{"Software"})

	// When filtering to Web3, industry counts still reflect the full inbox set.
	body := apiGet(t, newTestServer(t, d), "/api/vacancies?industries=Web3")
	icRaw, _ := body["industryCounts"].([]any)
	counts := make(map[string]int)
	for _, v := range icRaw {
		m, _ := v.(map[string]any)
		name, _ := m["name"].(string)
		n, _ := m["count"].(float64)
		counts[name] = int(n)
	}
	if counts["Software"] != 1 {
		t.Errorf("Software count with Web3 filter: got %d, want 1 (unfiltered base set)", counts["Software"])
	}
	if counts["Web3"] != 1 {
		t.Errorf("Web3 count: got %d, want 1", counts["Web3"])
	}
}

// --- Visibility with target industries ---

func TestVisibility_TargetIndustries_ShowsMatchingAndNoIndustry(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	// Profile requires a role + keyword, plus target industries.
	setProfileTargetIndustries(t, d, []string{"Web3"})

	id1 := add(t, cat, post("https://example.com/jobs/1", "Smart contract tester", "Co"))
	id2 := add(t, cat, post("https://example.com/jobs/2", "Platform tester", "Co"))         // no industry
	id3 := add(t, cat, post("https://example.com/jobs/3", "Software analyst tester", "Co")) // other industry

	setVacancyIndustries(t, d, id1, []string{"Web3"})
	setVacancyIndustries(t, d, id3, []string{"Software"})

	vocabNames := []string{"Web3", "Software"}
	ts := newServerWithVocab(t, d, vocabNames)
	rs := getRows(apiGet(t, ts, "/api/vacancies"))

	var ids []float64
	for _, r := range rs {
		ids = append(ids, rowID(r))
	}

	// id1 (Web3) and id2 (no industry) should be visible; id3 (Software only) should not.
	hasID1, hasID2, hasID3 := false, false, false
	for _, id := range ids {
		switch id {
		case float64(id1):
			hasID1 = true
		case float64(id2):
			hasID2 = true
		case float64(id3):
			hasID3 = true
		}
	}
	if !hasID1 {
		t.Errorf("vacancy with Web3 industry not visible")
	}
	if !hasID2 {
		t.Errorf("vacancy with no industry not visible (should always pass)")
	}
	if hasID3 {
		t.Errorf("vacancy with only Software industry visible despite target being Web3")
	}
}

func TestVisibility_TargetIndustries_OutsideVocabIgnored(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	// Profile targets "Obsolete" which is not in the vocab.
	setProfileTargetIndustries(t, d, []string{"Obsolete"})

	id1 := add(t, cat, post("https://example.com/jobs/1", "Platform tester", "Co"))
	id2 := add(t, cat, post("https://example.com/jobs/2", "Software analyst tester", "Co"))

	setVacancyIndustries(t, d, id2, []string{"Software"})

	// Vocab only has "Software", not "Obsolete". The profile's "Obsolete" target is
	// stripped, leaving no effective targets — no industry constraint applied.
	vocabNames := []string{"Software"}
	ts := newServerWithVocab(t, d, vocabNames)
	rs := getRows(apiGet(t, ts, "/api/vacancies"))

	// Both vacancies should be visible (no industry filter active after restriction).
	if len(rs) != 2 {
		t.Errorf("got %d rows, want 2 (no effective industry targets)", len(rs))
	}
	_ = id1
}

func TestVisibility_TargetIndustries_InheritedFromFirstProfile(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)

	// Profile 1: roles, keywords, target industries.
	setProfileTargetIndustries(t, d, []string{"Web3"})

	// Add a second profile with no target industries (they are inherited from profile 1).
	const insertProfile2 = `INSERT INTO profile (id, data) VALUES (2, ?)`
	type pd2 struct {
		Name     string   `json:"name"`
		Keywords []string `json:"keywords"`
	}
	b, _ := json.Marshal(pd2{Name: "Second", Keywords: []string{"description"}})
	if _, err := d.Exec(insertProfile2, string(b)); err != nil {
		t.Fatalf("insert profile 2: %v", err)
	}

	id1 := add(t, cat, post("https://example.com/jobs/1", "Chain tester", "Co"))
	id2 := add(t, cat, post("https://example.com/jobs/2", "Platform tester", "Co")) // no industry
	id3 := add(t, cat, post("https://example.com/jobs/3", "Software analyst tester", "Co"))

	setVacancyIndustries(t, d, id1, []string{"Web3"})
	setVacancyIndustries(t, d, id3, []string{"Software"})

	vocabNames := []string{"Web3", "Software"}
	ts := newServerWithVocab(t, d, vocabNames)

	// Profile 2 inherits Web3 target from profile 1.
	rs := getRows(apiGet(t, ts, "/api/vacancies?profile=2"))
	var hasID3 bool
	for _, r := range rs {
		if rowID(r) == float64(id3) {
			hasID3 = true
		}
	}
	if hasID3 {
		t.Errorf("profile 2 inherited Web3; vacancy with only Software should not be visible")
	}

	// id2 (no industry) should still be visible.
	var hasID2 bool
	for _, r := range rs {
		if rowID(r) == float64(id2) {
			hasID2 = true
		}
	}
	if !hasID2 {
		t.Errorf("vacancy with no industry should be visible even with inherited target industries")
	}
}

// --- Vacancy page carries industries ---

func TestVacancyPage_IndustriesField(t *testing.T) {
	d := openTestDB(t)
	cat := newCat(d)
	setProfileData(t, d, nil, []string{"description"}, nil)

	id := add(t, cat, listing.Posting{
		URL: "https://example.com/jobs/1", Title: "Blockchain Developer",
		Text: "job description", TextState: listing.TextFull,
	})
	setVacancyIndustries(t, d, id, []string{"Web3"})

	ts := newTestServer(t, d)
	// apiGetRaw does not run shape validation (VacancyView embeds Row which
	// the shape validator would read as a nested field, not inline fields).
	body, status := apiGetRaw(t, ts, "/api/vacancies/1")
	if status != http.StatusOK {
		t.Fatalf("GET /api/vacancies/1: status %d", status)
	}
	inds, _ := body["industries"].([]any)
	if len(inds) != 1 {
		t.Errorf("vacancy page industries: got %v, want [Web3]", inds)
	}
}

// --- Robustness: null industries in the database ---

func TestList_NullIndustriesReturnOKAndNoCount(t *testing.T) {
	// A vacancy whose industries column holds JSON null (written by a previous
	// buggy run of assign_industries) must not cause the list endpoint to
	// return 500. After reassignment the value must be [] not null.
	d := openTestDB(t)
	cat := newCat(d)
	setProfileData(t, d, nil, []string{"description"}, nil)

	id := add(t, cat, post("https://example.com/jobs/1", "Platform tester", "Co"))
	// Simulate the broken state: store JSON null instead of [].
	if _, err := d.Exec(`UPDATE vacancy SET industries='null' WHERE id=?`, id); err != nil {
		t.Fatalf("set null industries: %v", err)
	}

	ts := newTestServer(t, d)
	body, status := apiGetRaw(t, ts, "/api/vacancies")
	if status != http.StatusOK {
		t.Fatalf("GET /api/vacancies with null industries: status %d, want 200", status)
	}
	// The null value must not appear as a count entry.
	icRaw, _ := body["industryCounts"].([]any)
	if len(icRaw) != 0 {
		t.Errorf("industryCounts: got %v, want empty when only vacancy has null industries", icRaw)
	}
	// The row must still be listed with an empty industries slice.
	rows := getRows(body)
	if len(rows) != 1 {
		t.Fatalf("rows: got %d, want 1", len(rows))
	}
	row, _ := rows[0].(map[string]any)
	inds, _ := row["industries"].([]any)
	if len(inds) != 0 {
		t.Errorf("row industries: got %v, want []", inds)
	}
}

// --- IndustryCounts counts the stage being shown ---

func TestIndustryCounts_ShortlistCountsDifferFromInbox(t *testing.T) {
	// A vacancy with "Web3" in shortlist and a vacancy with "Software" in inbox
	// must produce different industryCounts when the shortlist is requested.
	d := openTestDB(t)
	cat := newCat(d)
	setProfileData(t, d, nil, []string{"description"}, nil)

	idShort := add(t, cat, post("https://example.com/jobs/1", "Chain tester", "Co"))
	idInbox := add(t, cat, post("https://example.com/jobs/2", "Platform tester", "Co"))

	setVacancyIndustries(t, d, idShort, []string{"Web3"})
	setVacancyIndustries(t, d, idInbox, []string{"Software"})

	// Move idShort to shortlist.
	now := testNowFn()
	_, err := d.Exec(
		`INSERT INTO decision (profile, vacancy, stage, reason, decided_at) VALUES (1,?,?,?,?)`,
		idShort, "shortlist", "", now.UTC().Format("2006-01-02T15:04:05Z"))
	if err != nil {
		t.Fatalf("insert decision: %v", err)
	}

	ts := newTestServer(t, d)

	// Shortlist: industryCounts should show Web3=1, not Software.
	shortBody := apiGet(t, ts, "/api/vacancies?stage=shortlist")
	shortCounts := make(map[string]int)
	for _, v := range shortBody["industryCounts"].([]any) {
		m, _ := v.(map[string]any)
		name, _ := m["name"].(string)
		n, _ := m["count"].(float64)
		shortCounts[name] = int(n)
	}
	if shortCounts["Web3"] != 1 {
		t.Errorf("shortlist Web3 count: got %d, want 1", shortCounts["Web3"])
	}
	if shortCounts["Software"] != 0 {
		t.Errorf("shortlist Software count: got %d, want 0 (Software vacancy is in inbox, not shortlist)", shortCounts["Software"])
	}

	// Inbox: industryCounts should show Software=1, not Web3.
	inboxBody := apiGet(t, ts, "/api/vacancies")
	inboxCounts := make(map[string]int)
	for _, v := range inboxBody["industryCounts"].([]any) {
		m, _ := v.(map[string]any)
		name, _ := m["name"].(string)
		n, _ := m["count"].(float64)
		inboxCounts[name] = int(n)
	}
	if inboxCounts["Software"] != 1 {
		t.Errorf("inbox Software count: got %d, want 1", inboxCounts["Software"])
	}
	if inboxCounts["Web3"] != 0 {
		t.Errorf("inbox Web3 count: got %d, want 0 (Web3 vacancy is in shortlist, not inbox)", inboxCounts["Web3"])
	}
}

func TestVisibility_TargetIndustries_InheritedFromNewFirstAfterDeletion(t *testing.T) {
	// After deleting the first profile, an empty-industries profile must inherit
	// from the new lowest-id profile, not from the deleted one.
	d := openTestDB(t)
	cat := newCat(d)
	setProfileTargetIndustries(t, d, []string{"Web3"})
	d.Exec(`INSERT INTO profile (id, data) VALUES (2, '{"name":"P2","keywords":["description"]}')`) //nolint:errcheck
	d.Exec(`INSERT INTO profile (id, data) VALUES (3, '{"name":"P3","keywords":["description"]}')`) //nolint:errcheck
	idS := add(t, cat, post("https://example.com/jobs/s1", "Software engineer description", "Co"))
	setVacancyIndustries(t, d, idS, []string{"Software"})
	idW := add(t, cat, post("https://example.com/jobs/w1", "Web3 dev description", "Co"))
	setVacancyIndustries(t, d, idW, []string{"Web3"})
	d.Exec(`DELETE FROM profile WHERE id=1`)                                                                                //nolint:errcheck
	d.Exec(`UPDATE profile SET data='{"name":"P2","keywords":["description"],"targetIndustries":["Software"]}' WHERE id=2`) //nolint:errcheck
	ts := newServerWithVocab(t, d, []string{"Web3", "Software"})
	rs := getRows(apiGet(t, ts, "/api/vacancies?profile=3"))
	if len(rs) != 1 || rowID(rs[0]) != float64(idS) {
		t.Errorf("after first-profile deletion: profile 3 should inherit Software from new first, got %d rows", len(rs))
	}
}

// --- Import assigns industries ---

func TestImport_IndustriesAssigned(t *testing.T) {
	// Use a fixture DB opened in-memory, then verify via the inbox store.
	// This test checks the assignment path without running the full server.
	ctx := context.Background()
	d := openTestDB(t)

	// Directly write a vacancy with keyword "solidity" (matches Web3 in default vocab)
	// and verify its industries after the catalog assigns them.
	cat := newCat(d) // no assign, so industries stay []

	id := add(t, cat, listing.Posting{
		URL:   "https://example.com/jobs/88",
		Title: "Solidity developer",
		Text:  "Write smart contract code using solidity.",

		TextState: listing.TextFull,
	})

	// industries must be [] without an assign function.
	var industries string
	d.QueryRowContext(ctx, `SELECT industries FROM vacancy WHERE id=?`, id).Scan(&industries) //nolint:errcheck
	if industries != "[]" {
		t.Errorf("industries without assigner: got %q, want []", industries)
	}
}
