package importv1_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const oldFitSchema = `
CREATE TABLE match_fit_results(
  profile_id TEXT NOT NULL, job_id INTEGER NOT NULL, user_revision INTEGER NOT NULL,
  profile_revision INTEGER NOT NULL, rules_identity TEXT NOT NULL, description_hash TEXT NOT NULL,
  fits_raw REAL, you_raw REAL, schema_version TEXT NOT NULL, record TEXT NOT NULL,
  computed_at TEXT NOT NULL, PRIMARY KEY(profile_id, job_id));
CREATE TABLE match_vacancy_extractions(job_id INTEGER, source_hash TEXT, extractor_version TEXT,
  requirements_hash TEXT, requirement_count INTEGER, schema_version TEXT, record TEXT, created_at TEXT);
CREATE TABLE match_job_facts(job_id INTEGER PRIMARY KEY, facts_hash TEXT, raw_source_hash TEXT,
  schema_version TEXT, extractor_version TEXT, source_policy_version TEXT, facts TEXT, updated_at TEXT);
`

// oldRecord is a breakdown of the previous version: role, location, remote
// restriction and work mode matched, money, time zone and engagement unknown,
// with some criteria changed ("money": "1,true").
func oldRecord(score int, changes map[string]string) string {
	groups := map[string]string{"role.title": "1,true,0.25", "compensation.cash": "0.5,false,0.2", "geography.location": "1,true,0.075",
		"geography.remote_restriction": "1,true,0.075", "conditions.timezone": "0.5,false,0.1", "conditions.work_mode": "1,true,0.04",
		"conditions.engagement": "0.5,false,0.03"}
	var list []string
	for c, v := range groups {
		if changed, ok := changes[c]; ok {
			v = changed
		}
		f := strings.Split(v, ",")
		list = append(list, `{"criterion":"`+c+`","utility":`+f[0]+`,"known":`+f[1]+`,"weight":`+f[2]+`}`)
	}
	return fmt.Sprintf(`{"fitsYou":{"status":"scored","score":%d,"groups":[%s]}}`, score, strings.Join(list, ","))
}

func TestImport_FitsYouComparedWithPreviousResultsByCause(t *testing.T) {
	from := makeOldDir(t, "")
	old := openOldDB(t, from)
	if _, err := old.Exec(oldFitSchema); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(from, "config")
	os.WriteFile(filepath.Join(cfg, "runtime.yaml"), []byte(`quick_match:
  version: 1
  criterion_weights: {role.title: 0.25, compensation.cash: 0.20, geography.location: 0.075,
    geography.remote_restriction: 0.075, conditions.work_mode: 0.04, conditions.engagement: 0.03,
    conditions.hours: 0, conditions.timezone: 0.10}
`), 0600)
	os.WriteFile(filepath.Join(cfg, "vacancy-view.yaml"), []byte("exchangeRates: {EUR: 1.0, USD: 0.9}\n"), 0600)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := old.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO users (id, revision, data, created_at, updated_at) VALUES ('u', 3, '{}', 'x', 'x')`)
	insertProfile(t, old, "p1", "Main", "2024-01-01T00:00:00Z", false)
	insertProfile(t, old, "p2", "Old", "2024-01-02T00:00:00Z", true)
	exec(`UPDATE user_profiles SET revision=2, data=? WHERE id='p1'`, `{"targetRoles":["Go Engineer"],
		"workModes":{"modes":["remote"]},"compensation":{"cashMin":90000,"currency":"EUR","period":"year"},
		"geography":{"preferredLocationsSet":true,"preferredLocations":[{"country":"DE"}]}}`)
	const remote = "We build tools.\nWork mode: Remote"
	model := `{"conditions":{"compensation":{"value":"EUR 100,000 per year","quote":"a hundred thousand"}}}`
	unavailable := `{"fitsYou":{"status":"unavailable","groups":[]}}`
	cases := []struct {
		job                           int64
		profile                       string
		rev                           int
		description, status, location string
		record, extraction            string
	}{
		{1, "p1", 2, remote, "available", "", oldRecord(79, nil), ""},                                             // equal; a single newline keeps the label
		{2, "p1", 2, remote, "feed description; may be excerpt", "", oldRecord(79, nil), ""},                      // a feed text is full
		{3, "p1", 2, "Go Engineer", "available structured description", "", unavailable, ""},                      // S1
		{4, "p1", 2, "", "available structured description", "", unavailable, ""},                                 // no text either side
		{5, "p1", 2, remote + "\nSalary: EUR 80,000 - 100,000 per year", "available", "", oldRecord(79, nil), ""}, // E11
		{6, "p1", 2, remote, "available", "Berlin, Germany", oldRecord(66, map[string]string{
			"geography.location": "0.5,false,0.075", "geography.remote_restriction": "0.5,false,0.075", "conditions.work_mode": "0.5,false,0.04"}), ""}, // E12
		{7, "p1", 2, remote, "available", "", oldRecord(92, map[string]string{"compensation.cash": "1,true,0.2"}), model},                                     // E1
		{8, "p1", 2, remote + "\nSalary: EUR 72,000 per year", "available", "", oldRecord(92, map[string]string{"compensation.cash": "1,true,0.2"}), model},   // read now: unexplained
		{9, "p1", 2, remote + "\n- Salary: EUR 100,000 per year", "available", "", oldRecord(79, nil), ""},                                                    // E3
		{10, "p1", 2, remote + "\nSalary: EUR 80,000 - 100,000 per year", "available", "", oldRecord(46, map[string]string{"role.title": "0,true,0.25"}), ""}, // one cause of two
		{13, "p1", 2, "We build tools.", "available", "Remote", oldRecord(74, map[string]string{"geography.remote_restriction": "0.5,false,0.075"}), ""},      // E4
		{14, "p1", 2, remote, "available", "", oldRecord(80, map[string]string{"role.title": "1,true,0.3"}), ""},                                              // W1
		{11, "p1", 1, remote, "available", "", oldRecord(10, nil), ""},                                                                                        // older profile revision: not current
		{12, "p2", 1, remote, "available", "", oldRecord(79, nil), ""},                                                                                        // archived profile: not comparable
	}
	for _, c := range cases {
		insertJob(t, old, c.job, fmt.Sprintf("https://jobs.example.org/role/%d", c.job))
		exec(`UPDATE jobs SET title='Go Engineer', description=?, description_status=?, location=? WHERE id=?`,
			c.description, c.status, c.location, c.job)
		sum := sha256.Sum256([]byte(c.description))
		exec(`INSERT INTO match_fit_results VALUES (?,?,3,?,'rules-a',?,NULL,NULL,'1',?,'x')`,
			c.profile, c.job, c.rev, hex.EncodeToString(sum[:]), c.record)
		if c.extraction != "" {
			exec(`INSERT INTO match_vacancy_extractions (job_id, record, created_at) VALUES (?,?,'x')`, c.job, c.extraction)
			exec(`INSERT INTO match_job_facts (job_id, facts) VALUES (?, '{"compensation":{}}')`, c.job)
		}
	}

	target := filepath.Join(t.TempDir(), "data")
	out := runImport(t, from, target)
	for _, want := range []string{
		"weights.hours: 0", "rates.USD: 0.9",
		"  equal: 3\n  different by expected difference: 7",
		"    E1 a condition only the old model read: 1\n    E3 a labelled line changed by the new normaliser: 1\n" +
			"    E4 the previous version did not use the board's location or tags: 1\n" +
			"    E11 money range containing the minimum: 1\n    E12 the previous version did not compare places (location 0.5): 1\n" +
			"    S1 number on one side only: the previous version read a text that only repeats the title or tags as unavailable: 1\n" +
			"    W1 same criteria, number differs: the old result was computed with other weights: 1\n" +
			"  different unexplained: 2\n    role: 1\n    money: 1\n  not comparable: 1\n  old results not current (not compared): 1\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
	tsv, err := os.ReadFile(filepath.Join(target, "fit-comparison.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	want := `profile	vacancy	old	new	criteria	code
1	3	no_text	66	state=S1	S1
1	5	79	92	money=E11	E11
1	6	66	71	location=E12	E12
1	7	92	79	money=E1	E1
1	8	92	86	money=?	unexplained
1	9	79	92	money=E3	E3
1	10	46	92	role=?,money=E11	unexplained
1	13	74	79	remote_restriction=E4	E4
1	14	80	79	number=W1	W1
`
	if string(tsv) != want {
		t.Errorf("fit-comparison.tsv =\n%s\nwant\n%s", tsv, want)
	}
	settings, _ := os.ReadFile(filepath.Join(target, "sifty.yaml"))
	if !strings.Contains(string(settings), "hours: 0") || !strings.Contains(string(settings), "USD: 0.9") {
		t.Errorf("settings file lacks carried weights and rates:\n%s", settings)
	}
}
