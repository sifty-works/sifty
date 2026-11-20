package inbox_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/sifty-works/sifty-server/internal/candidate"
	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/reading"
)

func newSkillsServer(t *testing.T, d *db.DB, changed func()) *httptest.Server {
	t.Helper()
	return newServer(t, d, serverOpts{rules: defaultRules, changed: changed})
}

func addVacancyWithReading(t *testing.T, d *db.DB, url, title string, reqs []reading.Requirement) int64 {
	t.Helper()
	id := add(t, newCat(d), postWithText(url, title, "Acme", "job text"))
	b, _ := json.Marshal(reading.Record{Requirements: reqs})
	d.Exec(`UPDATE vacancy SET version=1 WHERE id=?`, id)
	d.Exec(`INSERT INTO vacancy_reading (vacancy_id, version, reader_version, record, created)
		VALUES (?,1,'test',?,datetime('now'))`, id, string(b))
	return id
}

func saveMeSkills(t *testing.T, d *db.DB, skills []candidate.Skill, absent []candidate.AbsentSkill) {
	t.Helper()
	me := candidate.MeData{Skills: skills, AbsentSkills: absent}
	if err := candidate.SaveMe(context.Background(), d, me); err != nil {
		t.Fatalf("saveMeSkills: %v", err)
	}
}

func meSummary(t *testing.T, d *db.DB) string {
	t.Helper()
	me, err := candidate.LoadMe(context.Background(), d)
	if err != nil {
		t.Fatalf("loadMe: %v", err)
	}
	var out []string
	for _, s := range me.Skills {
		out = append(out, fmt.Sprintf("skill %s %s", s.Name, s.Level))
	}
	for _, a := range me.AbsentSkills {
		out = append(out, "absent "+a.Name)
	}
	for _, c := range me.ConfirmedFacts {
		out = append(out, fmt.Sprintf("fact %s %v", c.Text, c.Have))
	}
	sort.Strings(out)
	return strings.Join(out, "; ")
}

func postSkills(t *testing.T, ts *httptest.Server, vacancyID int64, reqIndex int, have bool, chosen string) (map[string]any, int) {
	t.Helper()
	payload := map[string]any{
		"vacancyId":        vacancyID,
		"requirementIndex": reqIndex,
		"have":             have,
	}
	if chosen != "" {
		payload["chosenSkill"] = chosen
	}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/me/skills", bytes.NewReader(b))
	req.Host = "127.0.0.1:9999"
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /api/me/skills: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return out, resp.StatusCode
}

func reqVerdict(resp map[string]any, idx int) string {
	vv, _ := resp["vacancyView"].(map[string]any)
	band, _ := vv["band"].(map[string]any)
	groups, _ := band["groups"].([]any)
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		items, _ := gm["items"].([]any)
		for _, it := range items {
			im, _ := it.(map[string]any)
			if int(im["index"].(float64)) == idx {
				v, _ := im["verdict"].(string)
				return v
			}
		}
	}
	return ""
}

func reqCounter(resp map[string]any) string {
	vv, _ := resp["vacancyView"].(map[string]any)
	band, _ := vv["band"].(map[string]any)
	v, _ := band["requirementsText"].(string)
	return v
}

func TestSkillsDecision_RecordsTheAnswer(t *testing.T) {
	for i, tc := range []struct {
		name        string
		skills      []candidate.Skill
		absent      []candidate.AbsentSkill
		req         reading.Requirement
		have        bool
		chosen      string
		wantMe      string
		wantVerdict string
	}{
		{name: "a known skill is added at Proficient",
			req:  reading.Requirement{Text: "Golang expertise", Kind: "skill", Level: "required"},
			have: true, wantMe: "skill Go Proficient", wantVerdict: "✓"},
		{name: "only the chosen alternative is added",
			req:    reading.Requirement{Text: "Go or Rust", Kind: "skill", Level: "required", Alts: []string{"Go", "Rust"}},
			have:   true,
			chosen: "Rust", wantMe: "skill Rust Proficient", wantVerdict: "✓"},
		{name: "a requirement outside the vocabulary is confirmed as held",
			req:  reading.Requirement{Text: "Strong communication skills", Kind: "skill", Level: "required"},
			have: true, wantMe: "fact Strong communication skills true", wantVerdict: "✓"},
		{name: "a requirement outside the vocabulary is confirmed as missing",
			req:  reading.Requirement{Text: "Strong communication skills", Kind: "skill", Level: "required"},
			have: false, wantMe: "fact Strong communication skills false", wantVerdict: "✕"},
		{name: "a skill answered with minus moves to the absent list",
			skills: []candidate.Skill{{Name: "Python", Level: "Expert"}},
			req:    reading.Requirement{Text: "Python", Kind: "skill", Level: "required"},
			have:   false, wantMe: "absent Python", wantVerdict: "✕"},
		{name: "a skill answered with plus leaves the absent list",
			absent: []candidate.AbsentSkill{{Name: "Docker"}},
			req:    reading.Requirement{Text: "Docker", Kind: "skill", Level: "required"},
			have:   true, wantMe: "skill Docker Proficient", wantVerdict: "✓"},
		{name: "a chosen part of a compound requirement is confirmed",
			req:    reading.Requirement{Text: "Docker and Kubernetes", Kind: "skill", Level: "required"},
			have:   true,
			chosen: "Docker", wantMe: "fact Docker and Kubernetes true; skill Docker Proficient", wantVerdict: "✓"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := openTestDB(t)
			if tc.skills != nil || tc.absent != nil {
				saveMeSkills(t, d, tc.skills, tc.absent)
			}
			id := addVacancyWithReading(t, d, fmt.Sprintf("https://example.com/jobs/sk%d", i), "Role",
				[]reading.Requirement{tc.req})
			ts := newSkillsServer(t, d, nil)

			resp, status := postSkills(t, ts, id, 0, tc.have, tc.chosen)
			if status != http.StatusOK {
				t.Fatalf("POST /api/me/skills: status %d, body %v", status, resp)
			}
			if got := meSummary(t, d); got != tc.wantMe {
				t.Errorf("My data: got %q, want %q", got, tc.wantMe)
			}
			if got := reqVerdict(resp, 0); got != tc.wantVerdict {
				t.Errorf("verdict: got %q, want %q", got, tc.wantVerdict)
			}
			if notice, _ := resp["notice"].(string); notice == "" || notice == "Nothing new to add; your data is already up to date." {
				t.Errorf("notice: %q — expected something added", notice)
			}
			if ct := reqCounter(resp); ct == "Not checked yet" || ct == "" {
				t.Errorf("requirements counter after the answer: %q", ct)
			}
		})
	}
}

func TestSkillsDecision_secondAddDoesNotDuplicate(t *testing.T) {
	d := openTestDB(t)
	id := addVacancyWithReading(t, d, "https://example.com/jobs/sk5", "DevOps",
		[]reading.Requirement{{Text: "Docker", Kind: "skill", Level: "required"}})
	ts := newSkillsServer(t, d, nil)

	postSkills(t, ts, id, 0, true, "")
	resp, status := postSkills(t, ts, id, 0, true, "")
	if status != http.StatusOK {
		t.Fatalf("second add: status %d", status)
	}
	if notice, _ := resp["notice"].(string); notice != "Nothing new to add; your data is already up to date." {
		t.Errorf("second add notice: %q", notice)
	}
	if got := meSummary(t, d); got != "skill Docker Proficient" {
		t.Errorf("My data after the second add: got %q, want one Docker skill", got)
	}
}

func TestSkillsDecision_invalidInputRefused(t *testing.T) {
	d := openTestDB(t)
	id := addVacancyWithReading(t, d, "https://example.com/jobs/sk6", "X", nil)
	ts := newSkillsServer(t, d, nil)
	if _, s := postSkills(t, ts, id, 5, true, ""); s != http.StatusUnprocessableEntity {
		t.Errorf("bad index: got %d, want 422", s)
	}
	if _, s := postSkills(t, ts, 999999, 0, true, ""); s != http.StatusNotFound {
		t.Errorf("missing vacancy: got %d, want 404", s)
	}
}

func TestSkillsDecision_recomputeFitQueued(t *testing.T) {
	d := openTestDB(t)
	id := addVacancyWithReading(t, d, "https://example.com/jobs/sk7", "Infra",
		[]reading.Requirement{{Text: "Kubernetes", Kind: "skill", Level: "required"}})
	queued := 0
	ts := newSkillsServer(t, d, func() { queued++ })

	postSkills(t, ts, id, 0, true, "")
	if queued != 1 {
		t.Errorf("recompute_fit queued: %d times, want 1", queued)
	}
}

func TestSkillsDecision_requirementGroupsInBand(t *testing.T) {
	d := openTestDB(t)
	id := addVacancyWithReading(t, d, "https://example.com/jobs/sk8", "Backend", []reading.Requirement{
		{Text: "Go", Kind: "skill", Level: "required"},
		{Text: "Docker", Kind: "skill", Level: "preferred"},
	})
	ts := newSkillsServer(t, d, nil)

	resp, status := apiGetRaw(t, ts, fmt.Sprintf("/api/vacancies/%d?profile=1", id))
	if status != http.StatusOK {
		t.Fatalf("GET vacancy: status %d", status)
	}
	band, _ := resp["band"].(map[string]any)
	if groups, _ := band["groups"].([]any); len(groups) == 0 {
		t.Error("band.groups should not be empty when requirements are read")
	}
}
