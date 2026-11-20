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
	"testing"

	"github.com/sifty-works/sifty-server/internal/candidate"
	"github.com/sifty-works/sifty-server/internal/config"
	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/fit"
	"github.com/sifty-works/sifty-server/internal/inbox"
	"github.com/sifty-works/sifty-server/internal/listing"
	"github.com/sifty-works/sifty-server/internal/reading"
	"github.com/sifty-works/sifty-server/internal/server"
)

func newSkillsServer(t *testing.T, d *db.DB, changed func()) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := func() fit.Rules { c := config.Defaults(); return fit.Rules{Weights: c.Weights, Rates: c.Rates} }
	h := server.New(server.Options{Version: "test", Port: 9999, Logger: logger},
		func(mux *http.ServeMux) {
			inbox.RegisterRoutes(mux, d, testNowFn, logger, func() bool { return false }, nil, r, changed)
		})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func addVacancyWithReading(t *testing.T, d *db.DB, url, title string, reqs []reading.Requirement) int64 {
	t.Helper()
	cat := newCat(d)
	id := add(t, cat, listing.Posting{
		URL: url, Title: title, Company: "Acme",
		Text: "job text", TextState: listing.TextFull,
	})
	rec := reading.Record{Requirements: reqs}
	b, _ := json.Marshal(rec)
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

func TestSkillsDecision_addKnownSkillAddsAtProficientAndCounterChanges(t *testing.T) {
	d := openTestDB(t)
	reqs := []reading.Requirement{{Text: "Golang expertise", Kind: "skill", Level: "required"}}
	id := addVacancyWithReading(t, d, "https://example.com/jobs/sk1", "Go Dev", reqs)
	ts := newSkillsServer(t, d, nil)

	resp, status := postSkills(t, ts, id, 0, true, "")
	if status != http.StatusOK {
		t.Fatalf("POST /api/me/skills: status %d, body: %v", status, resp)
	}
	notice, _ := resp["notice"].(string)
	if notice == "" || notice == "Nothing new to add; your data is already up to date." {
		t.Errorf("notice: %q — expected something added", notice)
	}
	me, _ := candidate.LoadMe(context.Background(), d)
	found := false
	for _, s := range me.Skills {
		if s.Name == "Go" {
			found = true
			if s.Level != "Proficient" {
				t.Errorf("Go skill level: got %q, want Proficient", s.Level)
			}
		}
	}
	if !found {
		t.Errorf("Go not added to skills; skills: %v", me.Skills)
	}
	ct := reqCounter(resp)
	if ct == "Not checked yet" || ct == "" {
		t.Errorf("counter after add: %q", ct)
	}
}

func TestSkillsDecision_addWithChosenAlt(t *testing.T) {
	d := openTestDB(t)
	reqs := []reading.Requirement{{Text: "Go or Rust", Kind: "skill", Level: "required", Alts: []string{"Go", "Rust"}}}
	id := addVacancyWithReading(t, d, "https://example.com/jobs/sk2", "Systems Dev", reqs)
	ts := newSkillsServer(t, d, nil)

	resp, status := postSkills(t, ts, id, 0, true, "Rust")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	me, _ := candidate.LoadMe(context.Background(), d)
	for _, s := range me.Skills {
		if s.Name == "Go" {
			t.Error("Go should not be added when Rust was chosen")
		}
	}
	found := false
	for _, s := range me.Skills {
		if s.Name == "Rust" {
			found = true
		}
	}
	if !found {
		t.Error("Rust should have been added")
	}
	_ = resp
}

func TestSkillsDecision_addUnknownVocabAddsConfirmedAndVerdictBecomesCheck(t *testing.T) {
	d := openTestDB(t)
	reqs := []reading.Requirement{{Text: "Strong communication skills", Kind: "skill", Level: "required"}}
	id := addVacancyWithReading(t, d, "https://example.com/jobs/sk3", "PM Role", reqs)
	ts := newSkillsServer(t, d, nil)

	resp, status := postSkills(t, ts, id, 0, true, "")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	me, _ := candidate.LoadMe(context.Background(), d)
	found := false
	for _, c := range me.ConfirmedFacts {
		if c.Text == "Strong communication skills" && c.Have {
			found = true
		}
	}
	if !found {
		t.Errorf("confirmed fact not added; facts: %v", me.ConfirmedFacts)
	}
	if v := reqVerdict(resp, 0); v != "✓" {
		t.Errorf("verdict after +: got %q, want ✓", v)
	}
}

func TestSkillsDecision_removeUnknownVocabAddsConfirmedAndVerdictBecomesX(t *testing.T) {
	d := openTestDB(t)
	reqs := []reading.Requirement{{Text: "Strong communication skills", Kind: "skill", Level: "required"}}
	id := addVacancyWithReading(t, d, "https://example.com/jobs/sk3b", "PM Role B", reqs)
	ts := newSkillsServer(t, d, nil)

	resp, status := postSkills(t, ts, id, 0, false, "")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	me, _ := candidate.LoadMe(context.Background(), d)
	found := false
	for _, c := range me.ConfirmedFacts {
		if c.Text == "Strong communication skills" && !c.Have {
			found = true
		}
	}
	if !found {
		t.Errorf("confirmed not-have fact not added; facts: %v", me.ConfirmedFacts)
	}
	if v := reqVerdict(resp, 0); v != "✕" {
		t.Errorf("verdict after −: got %q, want ✕", v)
	}
}

func TestSkillsDecision_removeAddsToAbsentAndRemovesFromSkills(t *testing.T) {
	d := openTestDB(t)
	saveMeSkills(t, d, []candidate.Skill{{Name: "Python", Level: "Expert"}}, nil)
	reqs := []reading.Requirement{{Text: "Python", Kind: "skill", Level: "required"}}
	id := addVacancyWithReading(t, d, "https://example.com/jobs/sk4", "Python Dev", reqs)
	ts := newSkillsServer(t, d, nil)

	resp, status := postSkills(t, ts, id, 0, false, "")
	if status != http.StatusOK {
		t.Fatalf("status %d, body: %v", status, resp)
	}
	me, _ := candidate.LoadMe(context.Background(), d)
	for _, s := range me.Skills {
		if s.Name == "Python" {
			t.Error("Python should have been removed from skills")
		}
	}
	found := false
	for _, a := range me.AbsentSkills {
		if a.Name == "Python" {
			found = true
		}
	}
	if !found {
		t.Error("Python should have been added to absent skills")
	}
}

func TestSkillsDecision_addRemovesFromAbsentSkills(t *testing.T) {
	d := openTestDB(t)
	saveMeSkills(t, d, nil, []candidate.AbsentSkill{{Name: "Docker"}})
	reqs := []reading.Requirement{{Text: "Docker", Kind: "skill", Level: "required"}}
	id := addVacancyWithReading(t, d, "https://example.com/jobs/sk4b", "DevOps B", reqs)
	ts := newSkillsServer(t, d, nil)

	resp, status := postSkills(t, ts, id, 0, true, "")
	if status != http.StatusOK {
		t.Fatalf("status %d, body: %v", status, resp)
	}
	me, _ := candidate.LoadMe(context.Background(), d)
	for _, a := range me.AbsentSkills {
		if a.Name == "Docker" {
			t.Error("Docker should have been removed from absent skills after +")
		}
	}
	found := false
	for _, s := range me.Skills {
		if s.Name == "Docker" {
			found = true
		}
	}
	if !found {
		t.Error("Docker should have been added to skills")
	}
	_ = resp
}

func TestSkillsDecision_secondAddDoesNotDuplicate(t *testing.T) {
	d := openTestDB(t)
	reqs := []reading.Requirement{{Text: "Docker", Kind: "skill", Level: "required"}}
	id := addVacancyWithReading(t, d, "https://example.com/jobs/sk5", "DevOps", reqs)
	ts := newSkillsServer(t, d, nil)

	postSkills(t, ts, id, 0, true, "")
	resp, status := postSkills(t, ts, id, 0, true, "")
	if status != http.StatusOK {
		t.Fatalf("second add: status %d", status)
	}
	notice, _ := resp["notice"].(string)
	if notice != "Nothing new to add; your data is already up to date." {
		t.Errorf("second add notice: %q", notice)
	}
	me, _ := candidate.LoadMe(context.Background(), d)
	count := 0
	for _, s := range me.Skills {
		if s.Name == "Docker" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("Docker count: got %d, want 1", count)
	}
}

func TestSkillsDecision_invalidInputRefused(t *testing.T) {
	d := openTestDB(t)
	id := addVacancyWithReading(t, d, "https://example.com/jobs/sk6", "X", nil)
	ts := newSkillsServer(t, d, nil)
	if _, s := postSkills(t, ts, id, 5, true, ""); s != 422 {
		t.Errorf("bad index: got %d, want 422", s)
	}
	if _, s := postSkills(t, ts, 999999, 0, true, ""); s != 404 {
		t.Errorf("missing vacancy: got %d, want 404", s)
	}
}

func TestSkillsDecision_partialVocabAddsConfirmedFact(t *testing.T) {
	d := openTestDB(t)
	id := addVacancyWithReading(t, d, "https://example.com/jobs/sk9", "DevOps X",
		[]reading.Requirement{{Text: "Docker and Kubernetes", Kind: "skill", Level: "required"}})
	ts := newSkillsServer(t, d, nil)
	resp, status := postSkills(t, ts, id, 0, true, "Docker")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	if v := reqVerdict(resp, 0); v != "✓" {
		t.Errorf("verdict: got %q, want ✓", v)
	}
	me, _ := candidate.LoadMe(context.Background(), d)
	found := false
	for _, c := range me.ConfirmedFacts {
		if c.Text == "Docker and Kubernetes" && c.Have {
			found = true
		}
	}
	if !found {
		t.Errorf("confirmed fact not added; facts: %v", me.ConfirmedFacts)
	}
}

func TestSkillsDecision_recomputeFitQueued(t *testing.T) {
	d := openTestDB(t)
	reqs := []reading.Requirement{{Text: "Kubernetes", Kind: "skill", Level: "required"}}
	id := addVacancyWithReading(t, d, "https://example.com/jobs/sk7", "Infra", reqs)
	queued := 0
	ts := newSkillsServer(t, d, func() { queued++ })

	postSkills(t, ts, id, 0, true, "")
	if queued != 1 {
		t.Errorf("recompute_fit queued: %d times, want 1", queued)
	}
}

func TestSkillsDecision_requirementGroupsInBand(t *testing.T) {
	d := openTestDB(t)
	reqs := []reading.Requirement{
		{Text: "Go", Kind: "skill", Level: "required"},
		{Text: "Docker", Kind: "skill", Level: "preferred"},
	}
	id := addVacancyWithReading(t, d, "https://example.com/jobs/sk8", "Backend", reqs)
	ts := newSkillsServer(t, d, nil)

	resp, status := apiGetRaw(t, ts, fmt.Sprintf("/api/vacancies/%d?profile=1", id))
	if status != http.StatusOK {
		t.Fatalf("GET vacancy: status %d", status)
	}
	band, _ := resp["band"].(map[string]any)
	groups, _ := band["groups"].([]any)
	if len(groups) == 0 {
		t.Error("band.groups should not be empty when requirements are read")
	}
}
