package inbox_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/sifty-works/sifty-server/internal/db"
)

func setVacancyIndustries(t *testing.T, d *db.DB, id int64, industries []string) {
	t.Helper()
	b, _ := json.Marshal(industries)
	if _, err := d.Exec(`UPDATE vacancy SET industries=? WHERE id=?`, string(b), id); err != nil {
		t.Fatalf("setVacancyIndustries: %v", err)
	}
}

func setProfileTargetIndustries(t *testing.T, d *db.DB, industries []string) {
	t.Helper()
	writeProfile(t, d, 1, profileData{Name: "Profile", Keywords: []string{"description"}, Industries: industries})
}

func industryFixture(t *testing.T) (*db.DB, map[string]int64) {
	t.Helper()
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"description"}, nil)
	cat := newCat(d)
	ids := map[string]int64{}
	for i, tc := range []struct {
		title      string
		industries []string
	}{
		{"Chain Dev", []string{"Web3"}},
		{"Platform Eng", []string{"Software"}},
		{"DevOps Engineer", []string{"Software"}},
		{"Security Analyst", nil},
	} {
		id := add(t, cat, post(fmt.Sprintf("https://example.com/jobs/in%d", i), tc.title, "Co"))
		ids[tc.title] = id
		if tc.industries != nil {
			setVacancyIndustries(t, d, id, tc.industries)
		}
	}
	return d, ids
}

func TestList_IndustriesFilterAndCounts(t *testing.T) {
	d, _ := industryFixture(t)
	ts := newTestServer(t, d)
	for _, tc := range []struct {
		name, query, want string
	}{
		{"no filter keeps every role", "", "Chain Dev,DevOps Engineer,Platform Eng,Security Analyst"},
		{"one industry keeps only its roles", "?industries=Web3", "Chain Dev"},
		{"several industries keep the roles of each", "?industries=Web3,Software",
			"Chain Dev,DevOps Engineer,Platform Eng"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := apiGet(t, ts, "/api/vacancies"+tc.query)
			if got := sortedTitles(body); got != tc.want {
				t.Errorf("rows: got %q, want %q", got, tc.want)
			}
			if c := namedCounts(body, "industryCounts"); c["Software"] != 2 || c["Web3"] != 1 {
				t.Errorf("industryCounts: Software=%d Web3=%d, want 2 and 1 (the industries filter is ignored)",
					c["Software"], c["Web3"])
			}
		})
	}
}

func TestIndustries_RowAndPageFields(t *testing.T) {
	d, ids := industryFixture(t)
	setVacancyIndustries(t, d, ids["Chain Dev"], []string{"Web3", "Security"})
	ts := newTestServer(t, d)
	byID := map[float64][]any{}
	for _, r := range getRows(apiGet(t, ts, "/api/vacancies")) {
		m, _ := r.(map[string]any)
		inds, _ := m["industries"].([]any)
		byID[rowID(r)] = inds
	}
	if got := byID[float64(ids["Chain Dev"])]; len(got) != 2 {
		t.Errorf("row industries: got %v, want [Web3 Security]", got)
	}
	if got := byID[float64(ids["Security Analyst"])]; len(got) != 0 {
		t.Errorf("row without industries: got %v, want []", got)
	}
	body, status := apiGetRaw(t, ts, fmt.Sprintf("/api/vacancies/%d", ids["Chain Dev"]))
	if status != http.StatusOK {
		t.Fatalf("vacancy page: status %d", status)
	}
	if inds, _ := body["industries"].([]any); len(inds) != 2 {
		t.Errorf("vacancy page industries: got %v, want [Web3 Security]", inds)
	}
}

func TestList_NullIndustriesReturnOKAndNoCount(t *testing.T) {
	d := openTestDB(t)
	setProfileData(t, d, nil, []string{"description"}, nil)
	id := add(t, newCat(d), post("https://example.com/jobs/1", "Platform tester", "Co"))
	if _, err := d.Exec(`UPDATE vacancy SET industries='null' WHERE id=?`, id); err != nil {
		t.Fatalf("set null industries: %v", err)
	}

	body, status := apiGetRaw(t, newTestServer(t, d), "/api/vacancies")
	if status != http.StatusOK {
		t.Fatalf("GET /api/vacancies with null industries: status %d, want 200", status)
	}
	if icRaw, _ := body["industryCounts"].([]any); len(icRaw) != 0 {
		t.Errorf("industryCounts: got %v, want empty when only vacancy has null industries", icRaw)
	}
	rows := getRows(body)
	if len(rows) != 1 {
		t.Fatalf("rows: got %d, want 1", len(rows))
	}
	if inds, _ := rows[0].(map[string]any)["industries"].([]any); len(inds) != 0 {
		t.Errorf("row industries: got %v, want []", inds)
	}
}

func TestIndustryCounts_ShortlistCountsDifferFromInbox(t *testing.T) {
	d, ids := industryFixture(t)
	decide(t, d, ids["Chain Dev"], "shortlist", "")
	ts := newTestServer(t, d)

	short := namedCounts(apiGet(t, ts, "/api/vacancies?stage=shortlist"), "industryCounts")
	if short["Web3"] != 1 || short["Software"] != 0 {
		t.Errorf("shortlist counts: Web3=%d Software=%d, want 1 and 0", short["Web3"], short["Software"])
	}
	inbox := namedCounts(apiGet(t, ts, "/api/vacancies"), "industryCounts")
	if inbox["Web3"] != 0 || inbox["Software"] != 2 {
		t.Errorf("inbox counts: Web3=%d Software=%d, want 0 and 2", inbox["Web3"], inbox["Software"])
	}
}

func TestVisibility_TargetIndustries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, d *db.DB)
		query string
		want  string
	}{
		{"a target industry keeps its roles and the roles without an industry",
			func(t *testing.T, d *db.DB) { setProfileTargetIndustries(t, d, []string{"Web3"}) },
			"", "Chain tester,Platform tester"},
		{"a name outside the fixed list is ignored",
			func(t *testing.T, d *db.DB) { setProfileTargetIndustries(t, d, []string{"Obsolete"}) },
			"", "Chain tester,Platform tester,Software analyst tester"},
		{"a profile without targets inherits them from the first profile",
			func(t *testing.T, d *db.DB) {
				setProfileTargetIndustries(t, d, []string{"Web3"})
				writeProfile(t, d, 2, profileData{Name: "Second", Keywords: []string{"description"}})
			},
			"?profile=2", "Chain tester,Platform tester"},
		{"after the first profile is deleted the new first decides",
			func(t *testing.T, d *db.DB) {
				setProfileTargetIndustries(t, d, []string{"Web3"})
				writeProfile(t, d, 2, profileData{Name: "Second", Keywords: []string{"description"},
					Industries: []string{"Software"}})
				writeProfile(t, d, 3, profileData{Name: "Third", Keywords: []string{"description"}})
				d.Exec(`DELETE FROM profile WHERE id=1`)
			},
			"?profile=3", "Platform tester,Software analyst tester"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := openTestDB(t)
			cat := newCat(d)
			id1 := add(t, cat, post("https://example.com/jobs/ti1", "Chain tester", "Co"))
			add(t, cat, post("https://example.com/jobs/ti2", "Platform tester", "Co"))
			id3 := add(t, cat, post("https://example.com/jobs/ti3", "Software analyst tester", "Co"))
			setVacancyIndustries(t, d, id1, []string{"Web3"})
			setVacancyIndustries(t, d, id3, []string{"Software"})
			tc.setup(t, d)
			if got := sortedTitles(apiGet(t, newTestServer(t, d), "/api/vacancies"+tc.query)); got != tc.want {
				t.Errorf("rows: got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestImport_IndustriesAssigned(t *testing.T) {
	d := openTestDB(t)
	id := add(t, newCat(d), postWithText("https://example.com/jobs/88", "Solidity developer", "Co",
		"Write smart contract code using solidity."))
	var industries string
	d.QueryRowContext(context.Background(), `SELECT industries FROM vacancy WHERE id=?`, id).Scan(&industries)
	if industries != "[]" {
		t.Errorf("industries without assigner: got %q, want []", industries)
	}
}
