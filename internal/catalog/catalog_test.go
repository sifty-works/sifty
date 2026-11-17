package catalog_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sifty-works/sifty/internal/catalog"
	"github.com/sifty-works/sifty/internal/db"
	"github.com/sifty-works/sifty/internal/listing"
)

// openTestDB opens a temporary database with the embedded catalog migrations.
func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "test.db"), db.Migrations)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// fixedNow returns a constant time for deterministic tests.
func fixedNow() func() time.Time {
	t := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

// posting builds a minimal Posting for a given URL.
func posting(rawURL, title, company string) listing.Posting {
	return listing.Posting{
		URL:       rawURL,
		Title:     title,
		Company:   company,
		Text:      "Full text of the vacancy.",
		TextKind:  listing.Plain,
		TextState: listing.TextFull,
	}
}

// --- New vacancy ---

func TestUpsert_NewVacancy(t *testing.T) {
	d := openTestDB(t)
	cat := catalog.New(d, fixedNow())

	id, outcome, err := cat.Upsert(context.Background(), posting("https://example.com/jobs/1", "Engineer", "Acme"), "src1")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if outcome != catalog.OutcomeNew {
		t.Errorf("outcome: got %v, want OutcomeNew", outcome)
	}
	if id <= 0 {
		t.Errorf("id: got %d, want > 0", id)
	}
	var srcCount int
	if err := d.QueryRow(`SELECT count(*) FROM vacancy_source WHERE vacancy_id = ? AND source = ?`, id, "src1").Scan(&srcCount); err != nil {
		t.Fatalf("source query: %v", err)
	}
	if srcCount != 1 {
		t.Errorf("source rows: got %d, want 1", srcCount)
	}

	p2 := posting("https://example.com/jobs/3", "Designer", "")
	id2, _, err := cat.Upsert(context.Background(), p2, "src")
	if err != nil {
		t.Fatalf("empty company upsert: %v", err)
	}
	var company string
	if err := d.QueryRow(`SELECT company FROM vacancy WHERE id = ?`, id2).Scan(&company); err != nil {
		t.Fatalf("company query: %v", err)
	}
	if company != "Unknown" {
		t.Errorf("company: got %q, want %q", company, "Unknown")
	}
}

// --- Match by address ---

func TestUpsert_MatchedByAddress(t *testing.T) {
	d := openTestDB(t)
	cat := catalog.New(d, fixedNow())

	// Same posting: OutcomeUnchanged.
	p0 := posting("https://example.com/jobs/10", "Engineer", "Acme")
	cat.Upsert(context.Background(), p0, "src") //nolint:errcheck
	_, outcome, err := cat.Upsert(context.Background(), p0, "src")
	if err != nil {
		t.Fatalf("unchanged upsert: %v", err)
	}
	if outcome != catalog.OutcomeUnchanged {
		t.Errorf("unchanged outcome: got %v, want OutcomeUnchanged", outcome)
	}

	// Different text: OutcomeUpdated, version increments.
	p1 := posting("https://example.com/jobs/12", "Engineer", "Acme")
	id1, _, _ := cat.Upsert(context.Background(), p1, "src")
	p1b := p1
	p1b.Text = "Different content here."
	_, out1, err := cat.Upsert(context.Background(), p1b, "src")
	if err != nil {
		t.Fatalf("text update upsert: %v", err)
	}
	if out1 != catalog.OutcomeUpdated {
		t.Errorf("text update outcome: got %v, want OutcomeUpdated", out1)
	}
	var version int
	d.QueryRow(`SELECT version FROM vacancy WHERE id = ?`, id1).Scan(&version) //nolint:errcheck
	if version != 2 {
		t.Errorf("version: got %d, want 2", version)
	}

	// Field updates: title and merged source_fields; Unknown company does not replace.
	p2 := posting("https://example.com/jobs/14", "Old Title", "Acme")
	p2.Fields = map[string]string{"salary": "100k", "type": "full-time"}
	id2, _, _ := cat.Upsert(context.Background(), p2, "src")
	p2b := p2
	p2b.Title = "New Title"
	p2b.Company = "" // becomes Unknown; original company should stay
	p2b.Fields = map[string]string{"salary": "120k", "remote": "yes"}
	cat.Upsert(context.Background(), p2b, "src") //nolint:errcheck
	var title, company, fields string
	d.QueryRow(`SELECT title, company, source_fields FROM vacancy WHERE id = ?`, id2).
		Scan(&title, &company, &fields) //nolint:errcheck
	if title != "New Title" {
		t.Errorf("title: got %q, want New Title", title)
	}
	if company != "Acme" {
		t.Errorf("company: got %q, want Acme (Unknown should not replace)", company)
	}
	if !strings.Contains(fields, `"salary":"120k"`) || !strings.Contains(fields, `"type":"full-time"`) || !strings.Contains(fields, `"remote":"yes"`) {
		t.Errorf("fields merged incorrectly: %s", fields)
	}

	// last_seen advances on a repeat.
	t0 := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	t1 := time.Date(2024, 2, 1, 9, 0, 0, 0, time.UTC)
	call := 0
	cat2 := catalog.New(d, func() time.Time {
		call++
		if call == 1 {
			return t0
		}
		return t1
	})
	p3 := posting("https://example.com/jobs/11", "Engineer", "Acme")
	id3, _, _ := cat2.Upsert(context.Background(), p3, "src")
	cat2.Upsert(context.Background(), p3, "src") //nolint:errcheck
	var lastSeen string
	if err := d.QueryRow(`SELECT last_seen FROM vacancy WHERE id = ?`, id3).Scan(&lastSeen); err != nil {
		t.Fatalf("last_seen query: %v", err)
	}
	if lastSeen != t1.Format(time.RFC3339) {
		t.Errorf("last_seen: got %q, want %q", lastSeen, t1.Format(time.RFC3339))
	}
}

// --- Match by apply address ---

func TestUpsert_MatchedByApplyAddress(t *testing.T) {
	d := openTestDB(t)
	cat := catalog.New(d, fixedNow())

	applyURL := "https://jobs.lever.co/acme/550e8400-e29b-41d4-a716-446655440000"

	p1 := posting("https://acme.com/careers/engineer", "Engineer", "Acme")
	p1.ApplyURL = applyURL
	id1, _, _ := cat.Upsert(context.Background(), p1, "src1")

	// Same apply URL, different posting URL.
	p2 := posting("https://acme.jobs/engineer", "Engineer", "Acme")
	p2.ApplyURL = applyURL
	id2, outcome, err := cat.Upsert(context.Background(), p2, "src2")
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if id2 != id1 {
		t.Errorf("id: got %d, want %d (same vacancy)", id2, id1)
	}
	if outcome == catalog.OutcomeNew {
		t.Error("outcome: should not be OutcomeNew")
	}
	// A board posting whose URL is the ATS canonical of another posting's ApplyURL also matches.
	{
		ats := "https://jobs.ashbyhq.com/acme/550e8400-e29b-41d4-a716-446655440000"
		cat2 := catalog.New(openTestDB(t), fixedNow())
		agg := posting("https://aggregator.example/jobs/9", "Platform Engineer", "Acme")
		agg.ApplyURL = ats + "/application"
		id1, _, _ := cat2.Upsert(context.Background(), agg, "agg")
		board := posting(ats, "Platform Engineer", "acme")
		id2, out, err := cat2.Upsert(context.Background(), board, "board")
		if err != nil || id2 != id1 || out == catalog.OutcomeNew {
			t.Fatalf("board posting: id %d (want %d), outcome %v, err %v", id2, id1, out, err)
		}
	}
}

// --- Match by identity on another host ---

func TestUpsert_MatchedByIdentityOnAnotherHost(t *testing.T) {
	d := openTestDB(t)
	cat := catalog.New(d, fixedNow())

	p1 := posting("https://acme.com/jobs/engineer", "Software Engineer", "Acme Corp")
	id1, _, _ := cat.Upsert(context.Background(), p1, "src1")

	// Same identity, different host.
	p2 := posting("https://boards.greenhouse.io/acme/jobs/9999", "Software Engineer", "Acme Corp")
	id2, outcome, err := cat.Upsert(context.Background(), p2, "src2")
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if id2 != id1 {
		t.Errorf("id: got %d, want %d (same vacancy)", id2, id1)
	}
	if outcome == catalog.OutcomeNew {
		t.Error("outcome: should not be OutcomeNew")
	}
}

// --- No match cases ---

func TestUpsert_NoMatchCases(t *testing.T) {
	type spec struct{ url, title, company, applyURL string }
	cases := []struct {
		name   string
		p1, p2 spec
	}{
		{
			"same host same identity",
			spec{"https://acme.com/jobs/1", "Software Engineer", "Acme Corp", ""},
			spec{"https://acme.com/jobs/2", "Software Engineer", "Acme Corp", ""},
		},
		{
			"unknown company",
			spec{"https://acme.com/jobs/nm1", "Data Analyst", "", ""},
			spec{"https://other.com/jobs/nm1", "Data Analyst", "", ""},
		},
		{
			"conflicting apply URLs",
			spec{"https://acme.com/jobs/nm2", "Engineer", "Acme Corp", "https://jobs.lever.co/acme/550e8400-e29b-41d4-a716-446655440000"},
			spec{"https://board.com/acme/nm2", "Engineer", "Acme Corp", "https://jobs.lever.co/acme/660e8400-e29b-41d4-a716-446655440001"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := openTestDB(t)
			cat := catalog.New(d, fixedNow())
			p1 := posting(tc.p1.url, tc.p1.title, tc.p1.company)
			p1.ApplyURL = tc.p1.applyURL
			id1, _, _ := cat.Upsert(context.Background(), p1, "src")
			p2 := posting(tc.p2.url, tc.p2.title, tc.p2.company)
			p2.ApplyURL = tc.p2.applyURL
			id2, outcome, err := cat.Upsert(context.Background(), p2, "src")
			if err != nil {
				t.Fatalf("upsert: %v", err)
			}
			if id2 == id1 {
				t.Error("should not match")
			}
			if outcome != catalog.OutcomeNew {
				t.Errorf("outcome: got %v, want OutcomeNew", outcome)
			}
		})
	}
}

// --- Text replacement rules ---

func TestUpsert_TextReplacement(t *testing.T) {
	cases := []struct {
		url      string
		initText string
		init     listing.TextState
		newText  string
		new      listing.TextState
		wantOut  catalog.Outcome
	}{
		{"u20", "Full.", listing.TextFull, "Brief.", listing.TextSummary, catalog.OutcomeUnchanged},
		{"u21", "Full.", listing.TextFull, "Block.", listing.TextBlocked, catalog.OutcomeUnchanged},
		{"u22", "", listing.TextRemoved, "Full text.", listing.TextFull, catalog.OutcomeUpdated},
		{"u23", "", listing.TextNotFetched, "Full text.", listing.TextFull, catalog.OutcomeUpdated},
	}
	for _, tc := range cases {
		d := openTestDB(t)
		cat := catalog.New(d, fixedNow())
		p := posting("https://example.com/jobs/"+tc.url, "Engineer", "Acme")
		p.Text = tc.initText
		p.TextState = tc.init
		id, _, _ := cat.Upsert(context.Background(), p, "src")
		p.Text = tc.newText
		p.TextState = tc.new
		_, out, _ := cat.Upsert(context.Background(), p, "src")
		if out != tc.wantOut {
			t.Errorf("%s: outcome %v, want %v", tc.url, out, tc.wantOut)
		}
		var state string
		d.QueryRow(`SELECT text_state FROM vacancy WHERE id = ?`, id).Scan(&state) //nolint:errcheck
		if state != "full" {
			t.Errorf("%s: text_state %q, want full", tc.url, state)
		}
	}
	// A whitespace-only difference after normalization keeps the version.
	{
		d2 := openTestDB(t)
		p := posting("https://example.com/jobs/u31", "Analyst", "Acme")
		p.Text, p.TextKind, p.TextState = "<p>Headquarters:</p><p>Remote</p>", listing.HTML, listing.TextFull
		id, _, _ := catalog.New(d2, fixedNow()).Upsert(context.Background(), p, "src")
		p.Text = "<p>Headquarters: Remote</p>"
		if _, out, _ := catalog.New(d2, fixedNow()).Upsert(context.Background(), p, "src"); out != catalog.OutcomeUnchanged {
			t.Fatalf("whitespace-only change: outcome %v, want Unchanged", out)
		}
		var v int
		d2.QueryRow(`SELECT version FROM vacancy WHERE id = ?`, id).Scan(&v) //nolint:errcheck
		if v != 1 {
			t.Fatalf("whitespace-only change: version %d, want 1", v)
		}
	}
}

// --- Publication date rules ---

func TestUpsert_PublicationDateRules(t *testing.T) {
	d := openTestDB(t)
	cat := catalog.New(d, fixedNow())
	past := time.Date(2024, 1, 10, 0, 0, 0, 0, time.UTC)
	future := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	// Past date is stored; subsequent date does not replace it.
	p1 := posting("https://example.com/jobs/30", "Engineer", "Acme")
	p1.Published = past
	p1.PublishedBasis = "datePosted"
	id1, _, _ := cat.Upsert(context.Background(), p1, "src")
	var pubAt, pubBasis string
	d.QueryRow(`SELECT published_at, published_basis FROM vacancy WHERE id = ?`, id1).Scan(&pubAt, &pubBasis) //nolint:errcheck
	if pubAt == "" || pubBasis != "datePosted" {
		t.Errorf("published_at=%q basis=%q", pubAt, pubBasis)
	}
	p1b := p1
	p1b.Published = time.Date(2024, 1, 20, 0, 0, 0, 0, time.UTC)
	p1b.PublishedBasis = "pubDate"
	cat.Upsert(context.Background(), p1b, "src") //nolint:errcheck
	var pubAt2 string
	d.QueryRow(`SELECT published_at FROM vacancy WHERE id = ?`, id1).Scan(&pubAt2) //nolint:errcheck
	if pubAt2 != pubAt {
		t.Errorf("date replaced: %q → %q", pubAt, pubAt2)
	}
	// Future date is not stored.
	p2 := posting("https://example.com/jobs/32", "Engineer", "Acme")
	p2.Published = future
	id2, _, _ := cat.Upsert(context.Background(), p2, "src")
	var pubAt3 string
	d.QueryRow(`SELECT published_at FROM vacancy WHERE id = ?`, id2).Scan(&pubAt3) //nolint:errcheck
	if pubAt3 != "" {
		t.Errorf("future date stored: %q", pubAt3)
	}
	// Expiry is not cleared when posting has no expiry.
	p3 := posting("https://example.com/jobs/33", "Engineer", "Acme")
	p3.Expires = time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	id3, _, _ := cat.Upsert(context.Background(), p3, "src")
	p3b := p3
	p3b.Expires = time.Time{}
	cat.Upsert(context.Background(), p3b, "src") //nolint:errcheck
	var exp string
	d.QueryRow(`SELECT expires_at FROM vacancy WHERE id = ?`, id3).Scan(&exp) //nolint:errcheck
	if exp == "" {
		t.Error("expires_at cleared when posting had no expiry")
	}
}

// --- Body round trip ---

func TestBody_RoundTrip(t *testing.T) {
	d := openTestDB(t)
	cat := catalog.New(d, fixedNow())

	p := posting("https://example.com/jobs/40", "Engineer", "Acme")
	p.Text = "This is the full job description text."
	p.TextKind = listing.Plain
	id, _, err := cat.Upsert(context.Background(), p, "src")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	var body []byte
	if err := d.QueryRow(`SELECT body FROM vacancy WHERE id = ?`, id).Scan(&body); err != nil {
		t.Fatalf("query body: %v", err)
	}

	text, err := catalog.Body(body)
	if err != nil {
		t.Fatalf("Body: %v", err)
	}
	if text == "" {
		t.Error("Body: got empty string, want non-empty")
	}
}

// --- Tombstone ---

func TestUpsert_TombstonedURLIsRejected(t *testing.T) {
	d := openTestDB(t)
	cat := catalog.New(d, fixedNow())

	// URL tombstone: canonical key "url:<address>".
	canonURL := "https://example.com/jobs/60"
	d.Exec(`INSERT INTO tombstone (key) VALUES (?)`, "url:"+canonURL) //nolint:errcheck
	_, outcome, err := cat.Upsert(context.Background(), posting(canonURL, "Engineer", "Acme"), "src")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if outcome != catalog.OutcomeTombstoned {
		t.Errorf("url tombstone: got %v, want OutcomeTombstoned", outcome)
	}
	var count int
	d.QueryRow(`SELECT count(*) FROM vacancy`).Scan(&count) //nolint:errcheck
	if count != 0 {
		t.Errorf("vacancy count: got %d, want 0", count)
	}

	// Legacy tombstone: keyed by sha256 of the case-sensitive canonical URL.
	rawURL := "https://Example.COM/jobs/80"
	h := sha256.Sum256([]byte("url:" + rawURL))
	tombKey := "legacy:" + hex.EncodeToString(h[:])
	d.Exec(`INSERT INTO tombstone (key) VALUES (?)`, tombKey) //nolint:errcheck
	_, out2, err := cat.Upsert(context.Background(), posting(rawURL, "Engineer", "Acme"), "src")
	if err != nil {
		t.Fatalf("legacy upsert: %v", err)
	}
	if out2 != catalog.OutcomeTombstoned {
		t.Errorf("legacy tombstone: got %v, want OutcomeTombstoned", out2)
	}
}

// --- Apply URL ---

func TestUpsert_ApplyURLRules(t *testing.T) {
	d := openTestDB(t)
	cat := catalog.New(d, fixedNow())
	// Apply URL is set when it was previously empty.
	p1 := posting("https://example.com/jobs/70", "Engineer", "Acme")
	id1, _, _ := cat.Upsert(context.Background(), p1, "src")
	p1b := p1
	p1b.ApplyURL = "https://jobs.lever.co/acme/550e8400-e29b-41d4-a716-446655440000"
	cat.Upsert(context.Background(), p1b, "src") //nolint:errcheck
	var got1 string
	d.QueryRow(`SELECT apply_url FROM vacancy WHERE id = ?`, id1).Scan(&got1) //nolint:errcheck
	if got1 == "" {
		t.Error("apply_url: should be set after update")
	}
	// Apply URL is not overwritten when already set.
	orig := "https://jobs.lever.co/acme/550e8400-e29b-41d4-a716-446655440000"
	p2 := posting("https://example.com/jobs/71", "Engineer", "Acme")
	p2.ApplyURL = orig
	id2, _, _ := cat.Upsert(context.Background(), p2, "src")
	p2b := p2
	p2b.ApplyURL = "https://jobs.lever.co/acme/770e8400-e29b-41d4-a716-446655440002"
	cat.Upsert(context.Background(), p2b, "src") //nolint:errcheck
	var got2 string
	d.QueryRow(`SELECT apply_url FROM vacancy WHERE id = ?`, id2).Scan(&got2) //nolint:errcheck
	if got2 != orig {
		t.Errorf("apply_url overwritten: got %q, want %q", got2, orig)
	}
	// An apply address that is not one concrete ATS vacancy is not kept.
	p3 := posting("https://example.com/jobs/72", "Designer", "Acme")
	p3.ApplyURL = "https://apply.acmecorp.example/"
	id3, _, _ := cat.Upsert(context.Background(), p3, "src")
	var got3 string
	d.QueryRow(`SELECT apply_url FROM vacancy WHERE id = ?`, id3).Scan(&got3) //nolint:errcheck
	if got3 != "" {
		t.Errorf("apply_url kept for a non-ATS address: %q", got3)
	}
}

// --- Delete cascades (requires foreign_keys pragma) ---

func TestDeleteVacancy_CascadesToVacancySource(t *testing.T) {
	d := openTestDB(t)
	cat := catalog.New(d, fixedNow())

	id, _, err := cat.Upsert(context.Background(),
		posting("https://example.com/jobs/50", "Engineer", "Acme"), "src")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if _, err := d.Exec(`DELETE FROM vacancy WHERE id = ?`, id); err != nil {
		t.Fatalf("delete: %v", err)
	}

	var count int
	d.QueryRow(`SELECT count(*) FROM vacancy_source WHERE vacancy_id = ?`, id).Scan(&count) //nolint:errcheck
	if count != 0 {
		t.Errorf("vacancy_source rows after delete: got %d, want 0", count)
	}
}

// --- Merge ---
//
// Same-host vacancies with the same identity do not merge at insert time.
// A posting from a different host that matches both triggers a merge.

func TestUpsert_MergeVacancies(t *testing.T) {
	d := openTestDB(t)
	cat := catalog.New(d, fixedNow())

	// Two same-host vacancies: merge keeps the lowest ID.
	p1 := posting("https://acme.com/jobs/e1", "Site Reliability Engineer", "Acme Co")
	id1, out1, err := cat.Upsert(context.Background(), p1, "src1")
	if err != nil || out1 != catalog.OutcomeNew {
		t.Fatalf("first upsert: %v / %v", err, out1)
	}
	p2 := posting("https://acme.com/jobs/e2", "Site Reliability Engineer", "Acme Co")
	id2, out2, err := cat.Upsert(context.Background(), p2, "src2")
	if err != nil || out2 != catalog.OutcomeNew {
		t.Fatalf("second upsert: %v / %v", err, out2)
	}
	if id1 == id2 {
		t.Fatal("expected separate vacancies on same host")
	}
	keptID2, outcome2, err := cat.Upsert(context.Background(),
		posting("https://other.com/jobs/sre", "Site Reliability Engineer", "Acme Co"), "src3")
	if err != nil {
		t.Fatalf("2-way merge: %v", err)
	}
	if outcome2 != catalog.OutcomeMerged {
		t.Errorf("2-way outcome: got %v, want OutcomeMerged", outcome2)
	}
	wantKept := id1
	if id2 < id1 {
		wantKept = id2
	}
	if keptID2 != wantKept {
		t.Errorf("kept id: got %d, want %d (the lowest)", keptID2, wantKept)
	}

	// Three same-host vacancies: merge leaves one for that identity.
	for i, url := range []string{
		"https://zeta.com/jobs/t1",
		"https://zeta.com/jobs/t2",
		"https://zeta.com/jobs/t3",
	} {
		_, out, err := cat.Upsert(context.Background(),
			posting(url, "Platform Engineer", "Zeta Corp"), fmt.Sprintf("src%d", i+4))
		if err != nil || out != catalog.OutcomeNew {
			t.Fatalf("upsert %d: %v / %v", i+1, err, out)
		}
	}
	_, outcome3, err := cat.Upsert(context.Background(),
		posting("https://other.com/jobs/pe", "Platform Engineer", "Zeta Corp"), "src7")
	if err != nil {
		t.Fatalf("3-way merge: %v", err)
	}
	if outcome3 != catalog.OutcomeMerged {
		t.Errorf("3-way outcome: got %v, want OutcomeMerged", outcome3)
	}
	var count int
	d.QueryRow(`SELECT count(*) FROM vacancy`).Scan(&count) //nolint:errcheck
	if count != 2 {                                         // SRE kept + PE kept
		t.Errorf("vacancy count after 3-way merge: got %d, want 2", count)
	}
}

func TestUpsert_MergeTransfersBestText(t *testing.T) {
	d := openTestDB(t)
	cat := catalog.New(d, fixedNow())
	lowP := listing.Posting{URL: "https://c.io/j1", Title: "AI Dev", Company: "C",
		Text: "Brief.", TextKind: listing.Plain, TextState: listing.TextSummary}
	idLow, _, _ := cat.Upsert(context.Background(), lowP, "s1")
	highP := listing.Posting{URL: "https://c.io/j2", Title: "AI Dev", Company: "C",
		Text: "Full text.", TextKind: listing.Plain, TextState: listing.TextFull}
	idHigh, _, _ := cat.Upsert(context.Background(), highP, "s2")
	if idHigh <= idLow {
		t.Fatal("expected idHigh > idLow")
	}
	trigger := listing.Posting{URL: "https://ai.c.io/j", Title: "AI Dev", Company: "C", TextState: listing.TextNotFetched}
	_, out, _ := cat.Upsert(context.Background(), trigger, "s3")
	if out != catalog.OutcomeMerged {
		t.Fatalf("merge outcome: %v", out)
	}
	var state string
	var ver int
	d.QueryRow(`SELECT text_state, version FROM vacancy WHERE id = ?`, idLow).Scan(&state, &ver) //nolint:errcheck
	if state != "full" || ver != 2 {
		t.Errorf("text_state=%q ver=%d, want full/2", state, ver)
	}
}

func TestUpsert_MergeAliasRowsAndSources(t *testing.T) {
	d := openTestDB(t)
	cat := catalog.New(d, fixedNow())

	p1 := posting("https://acme.com/jobs/a1", "Data Engineer", "Gamma Ltd")
	id1, _, _ := cat.Upsert(context.Background(), p1, "src1")
	p2 := posting("https://acme.com/jobs/a2", "Data Engineer", "Gamma Ltd")
	id2, _, _ := cat.Upsert(context.Background(), p2, "src2")

	// Pre-insert an alias pointing to the higher-id vacancy;
	// after merge it should be repointed to keptID.
	var oldAlias int64 = 9999
	higherID := id2
	if id1 > id2 {
		higherID = id1
	}
	d.Exec(`INSERT INTO vacancy_alias (old_id, new_id) VALUES (?, ?)`, oldAlias, higherID) //nolint:errcheck

	p3 := posting("https://gamma-jobs.com/de", "Data Engineer", "Gamma Ltd")
	keptID, outcome, err := cat.Upsert(context.Background(), p3, "src3")
	if err != nil {
		t.Fatalf("merge upsert: %v", err)
	}
	if outcome != catalog.OutcomeMerged {
		t.Errorf("outcome: got %v, want OutcomeMerged", outcome)
	}

	// The merged vacancy has an alias row pointing to keptID.
	mergedID := id2
	if id2 == keptID {
		mergedID = id1
	}
	var newID int64
	if err := d.QueryRow(`SELECT new_id FROM vacancy_alias WHERE old_id = ?`, mergedID).Scan(&newID); err != nil {
		t.Fatalf("alias row not found for merged id %d: %v", mergedID, err)
	}
	if newID != keptID {
		t.Errorf("alias new_id: got %d, want %d", newID, keptID)
	}

	// The pre-existing alias is repointed to keptID.
	var newTarget int64
	d.QueryRow(`SELECT new_id FROM vacancy_alias WHERE old_id = ?`, oldAlias).Scan(&newTarget) //nolint:errcheck
	if newTarget != keptID {
		t.Errorf("old alias repointed: got %d, want %d", newTarget, keptID)
	}

	// Source rows from merged vacancies move to keptID.
	for _, mid := range []int64{id1, id2} {
		if mid == keptID {
			continue
		}
		var orphaned int
		d.QueryRow(`SELECT count(*) FROM vacancy_source WHERE vacancy_id = ?`, mid).Scan(&orphaned) //nolint:errcheck
		if orphaned != 0 {
			t.Errorf("source rows for merged id %d: got %d, want 0", mid, orphaned)
		}
	}
	var kept int
	d.QueryRow(`SELECT count(*) FROM vacancy_source WHERE vacancy_id = ?`, keptID).Scan(&kept) //nolint:errcheck
	if kept < 2 {
		t.Errorf("source rows for kept id %d: got %d, want >= 2", keptID, kept)
	}
}

// --- FTS index ---

// ftsHasRow reports whether vacancy_fts contains a row for id.
func ftsHasRow(t *testing.T, d *db.DB, id int64) bool {
	t.Helper()
	var n int
	if err := d.QueryRow(
		`SELECT count(*) FROM vacancy_fts WHERE rowid = ?`, id).Scan(&n); err != nil {
		t.Fatalf("fts count for %d: %v", id, err)
	}
	return n > 0
}

// ftsMatch reports whether vacancy_fts returns id for the given query phrase.
func ftsMatch(t *testing.T, d *db.DB, id int64, phrase string) bool {
	t.Helper()
	q := `"` + phrase + `"`
	var n int
	if err := d.QueryRow(
		`SELECT count(*) FROM vacancy_fts WHERE rowid = ? AND vacancy_fts MATCH ?`, id, q).Scan(&n); err != nil {
		t.Fatalf("fts match %d %q: %v", id, phrase, err)
	}
	return n > 0
}

func TestFTS_TextReplaced(t *testing.T) {
	d := openTestDB(t)
	cat := catalog.New(d, fixedNow())
	p := listing.Posting{
		URL: "https://example.com/jobs/fts2", Title: "Backend Engineer", Company: "Acme",
		Text:      "Experience with Kafka and streaming pipelines.",
		TextKind:  listing.Plain,
		TextState: listing.TextFull,
	}
	id, _, err := cat.Upsert(context.Background(), p, "src")
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if !ftsMatch(t, d, id, "kafka") {
		t.Error("FTS should match original body text")
	}

	// Replace text: different content.
	p2 := p
	p2.Text = "Must know PostgreSQL and query optimisation."
	_, outcome, err := cat.Upsert(context.Background(), p2, "src")
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if outcome != catalog.OutcomeUpdated {
		t.Fatalf("outcome: got %v, want OutcomeUpdated", outcome)
	}
	if ftsMatch(t, d, id, "kafka") {
		t.Error("old FTS text should be gone after replacement")
	}
	if !ftsMatch(t, d, id, "postgresql") {
		t.Error("new FTS text should be searchable after replacement")
	}
}

func TestFTS_Merge(t *testing.T) {
	d := openTestDB(t)
	cat := catalog.New(d, fixedNow())

	// Two vacancies on the same host: same identity, do not merge by identity on same host.
	p1 := listing.Posting{
		URL: "https://acme.com/jobs/ftsm1", Title: "DevOps Engineer", Company: "Acme Corp",
		Text:      "Summary only.",
		TextKind:  listing.Plain,
		TextState: listing.TextSummary,
	}
	id1, out1, err := cat.Upsert(context.Background(), p1, "src1")
	if err != nil || out1 != catalog.OutcomeNew {
		t.Fatalf("first upsert: %v / %v", err, out1)
	}
	p2 := listing.Posting{
		URL: "https://acme.com/jobs/ftsm2", Title: "DevOps Engineer", Company: "Acme Corp",
		Text:      "Full description mentioning containerisation and service meshes.",
		TextKind:  listing.Plain,
		TextState: listing.TextFull,
	}
	id2, out2, err := cat.Upsert(context.Background(), p2, "src2")
	if err != nil || out2 != catalog.OutcomeNew {
		t.Fatalf("second upsert: %v / %v", err, out2)
	}
	if id1 == id2 {
		t.Fatal("expected two separate vacancies on same host")
	}

	// Third upsert from a different host: same identity → merges id1 and id2.
	p3 := listing.Posting{
		URL: "https://other.com/jobs/devops", Title: "DevOps Engineer", Company: "Acme Corp",
		Text:      "Full description mentioning containerisation and service meshes.",
		TextKind:  listing.Plain,
		TextState: listing.TextFull,
	}
	keptID, outcome, err := cat.Upsert(context.Background(), p3, "src3")
	if err != nil {
		t.Fatalf("merge upsert: %v", err)
	}
	if outcome != catalog.OutcomeMerged {
		t.Fatalf("outcome: got %v, want OutcomeMerged", outcome)
	}

	// The merged (deleted) vacancies should have no FTS rows.
	for _, mid := range []int64{id1, id2} {
		if mid == keptID {
			continue
		}
		if ftsHasRow(t, d, mid) {
			t.Errorf("FTS row for merged vacancy %d should be removed", mid)
		}
	}

	// The kept vacancy should have the full-text FTS row.
	if !ftsMatch(t, d, keptID, "containerisation") {
		t.Errorf("FTS row for kept vacancy %d should contain full text", keptID)
	}
}
