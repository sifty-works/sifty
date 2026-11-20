package inbox_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/sifty-works/sifty-server/internal/fit"
	"github.com/sifty-works/sifty-server/internal/listing"
)

func reqText(r any) string {
	m, _ := r.(map[string]any)
	f, _ := m["fit"].(map[string]any)
	v, _ := f["requirementsText"].(string)
	return v
}

func bandReqText(body map[string]any) string {
	b, _ := body["band"].(map[string]any)
	v, _ := b["requirementsText"].(string)
	return v
}

func postWithText(url, title, company, text string) listing.Posting {
	return listing.Posting{URL: url, Title: title, Company: company, Text: text, TextState: listing.TextFull}
}

func TestRequirementsCounter_noTextState(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"description"}, nil)
	p, _ := fit.LoadProfile(context.Background(), d, 1, defaultRules())
	id := add(t, newCat(d), postWithText("https://example.com/jobs/ct1", "Role CT1", "Co", "job description"))
	d.Exec(fmt.Sprintf(`INSERT INTO fit (profile,vacancy,version,inputs,listing,state,score,req_ok,req_no,req_maybe)
		VALUES (1,%d,1,%d,0,'no_text',NULL,NULL,NULL,NULL)`, id, p.Inputs))
	ts := newTestServer(t, d)
	rows := getRows(apiGet(t, ts, "/api/vacancies"))
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if got := reqText(rows[0]); got != "Full job description is unavailable" {
		t.Errorf("no_text: got %q", got)
	}
}

func TestRequirementsCounter_notAnalysedYet(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"description"}, nil)
	add(t, newCat(d), postWithText("https://example.com/jobs/ct2", "Role CT2", "Co", "job description"))
	ts := newTestServer(t, d)
	rows := getRows(apiGet(t, ts, "/api/vacancies"))
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if got := reqText(rows[0]); got != "Not analysed yet" {
		t.Errorf("no fit row: got %q", got)
	}
}

func TestRequirementsCounter_analysingWhenJobQueued(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"description"}, nil)
	id := add(t, newCat(d), postWithText("https://example.com/jobs/ct3", "Role CT3", "Co", "job description"))
	d.Exec(`INSERT INTO job (kind,subject,state,priority,not_before,created,updated) VALUES ('read_vacancy',?,'queued',0,'','2026-11-01T00:00:00Z','2026-11-01T00:00:00Z')`,
		fmt.Sprintf("%d/1", id))
	ts := newTestServer(t, d)
	rows := getRows(apiGet(t, ts, "/api/vacancies"))
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if got := reqText(rows[0]); got != "Analysing requirements…" {
		t.Errorf("queued job: got %q", got)
	}
}

func TestRequirementsCounter_noRequirementsStated(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"description"}, nil)
	p, _ := fit.LoadProfile(context.Background(), d, 1, defaultRules())
	id := add(t, newCat(d), postWithText("https://example.com/jobs/ct4", "Role CT4", "Co", "job description"))
	d.Exec(fmt.Sprintf(`INSERT INTO fit (profile,vacancy,version,inputs,listing,state,score,req_ok,req_no,req_maybe)
		VALUES (1,%d,1,%d,0,'scored',75,0,0,0)`, id, p.Inputs))
	ts := newTestServer(t, d)
	rows := getRows(apiGet(t, ts, "/api/vacancies"))
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if got := reqText(rows[0]); got != "No requirements stated" {
		t.Errorf("zero total: got %q", got)
	}
}

func TestRequirementsCounter_notCheckedYetAllMaybe(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"description"}, nil)
	p, _ := fit.LoadProfile(context.Background(), d, 1, defaultRules())
	id := add(t, newCat(d), postWithText("https://example.com/jobs/ct5", "Role CT5", "Co", "job description"))
	d.Exec(fmt.Sprintf(`INSERT INTO fit (profile,vacancy,version,inputs,listing,state,score,req_ok,req_no,req_maybe)
		VALUES (1,%d,1,%d,0,'scored',75,0,0,4)`, id, p.Inputs))
	ts := newTestServer(t, d)
	rows := getRows(apiGet(t, ts, "/api/vacancies"))
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if got := reqText(rows[0]); got != "Not checked yet" {
		t.Errorf("all maybe: got %q", got)
	}
}

func TestRequirementsCounter_countsString(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"description"}, nil)
	p, _ := fit.LoadProfile(context.Background(), d, 1, defaultRules())
	id := add(t, newCat(d), postWithText("https://example.com/jobs/ct6", "Role CT6", "Co", "job description"))
	d.Exec(fmt.Sprintf(`INSERT INTO fit (profile,vacancy,version,inputs,listing,state,score,req_ok,req_no,req_maybe)
		VALUES (1,%d,1,%d,0,'scored',75,3,1,2)`, id, p.Inputs))
	ts := newTestServer(t, d)
	rows := getRows(apiGet(t, ts, "/api/vacancies"))
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if got := reqText(rows[0]); got != "✓ 3 · ✕ 1 · ? 2" {
		t.Errorf("counts string: got %q", got)
	}
}

func TestRequirementsCounter_bandNotAnalysedYet(t *testing.T) {
	d := openTestDB(t)
	id := add(t, newCat(d), postWithText("https://example.com/jobs/ct7", "Role CT7", "Co", "job description"))
	ts := newTestServer(t, d)
	body, _ := apiGetRaw(t, ts, fmt.Sprintf("/api/vacancies/%d?profile=1", id))
	if got := bandReqText(body); got != "Not analysed yet" {
		t.Errorf("band no reading: got %q", got)
	}
	band, _ := body["band"].(map[string]any)
	if groups, ok := band["groups"].([]any); !ok || len(groups) != 0 {
		t.Errorf("band groups without a reading: want an empty list, got %v", band["groups"])
	}
}

func TestRequirementsCounter_bandAnalysingWhenJobQueued(t *testing.T) {
	d := openTestDB(t)
	id := add(t, newCat(d), postWithText("https://example.com/jobs/ct8", "Role CT8", "Co", "job description"))
	d.Exec(`INSERT INTO job (kind,subject,state,priority,not_before,created,updated) VALUES ('read_vacancy',?,'queued',0,'','2026-11-01T00:00:00Z','2026-11-01T00:00:00Z')`,
		fmt.Sprintf("%d/1", id))
	ts := newTestServer(t, d)
	body, _ := apiGetRaw(t, ts, fmt.Sprintf("/api/vacancies/%d?profile=1", id))
	if got := bandReqText(body); got != "Analysing requirements…" {
		t.Errorf("band queued: got %q", got)
	}
}
