package importv1

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/sifty-works/sifty-server/internal/candidate"
	"github.com/sifty-works/sifty-server/internal/catalog"
	"github.com/sifty-works/sifty-server/internal/config"
	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/fit"
	"github.com/sifty-works/sifty-server/internal/places"
	"github.com/sifty-works/sifty-server/internal/reading"
	"gopkg.in/yaml.v3"
)

var oldCriteria = map[string]string{
	"role.title": "role", "compensation.cash": "money", "geography.location": "location",
	"geography.remote_restriction": "remote_restriction", "conditions.timezone": "time_zone",
	"conditions.work_mode": "work_mode", "conditions.engagement": "engagement", "conditions.hours": "hours",
}

var expectedDifferences = []struct{ code, text string }{
	{"E2", "place given only as a city"},
	{"E3", "a labelled line changed by the new normaliser"},
	{"E4", "the previous version did not use the board's location or tags"},
	{"E5", "merged duplicates"},
	{"E6", "restricted remote without stated authorization now unknown"},
	{"E7", "unrestricted remote now 1 on remote restriction without user geography"},
	{"E8", "a profile field folded at import"},
	{"E9", "a time zone the old version read from free-text phrases"},
	{"E10", "weekly-hours ranges"},
	{"E11", "money range containing the minimum"},
	{"E12", "the previous version did not compare places (location 0.5)"},
	{"E13", "the old extraction had a condition the old facts did not confirm, so the old score treated it as unknown"},
	{"S1", "number on one side only: the previous version read a text that only repeats the title or tags as unavailable"},
	{"S2", "number on one side only: merged duplicates"},
	{"W1", "same criteria, number differs: the old result was computed with other weights"},
}

type fitComparison struct {
	Found                       bool
	Equal, Unexplained, NotComp int
	Expected, NotCurrent        int
	ByCode                      map[string]int
	UnexplainedBy               map[string]int
}

func importRules(fromDir string, store *config.Store, report *importReport) error {
	var runtime struct {
		QuickMatch struct {
			Version          int                `yaml:"version"`
			CriterionWeights map[string]float64 `yaml:"criterion_weights"`
		} `yaml:"quick_match"`
	}
	var view struct {
		ExchangeRates map[string]float64 `yaml:"exchangeRates"`
	}
	if b, err := os.ReadFile(filepath.Join(fromDir, "config", "runtime.yaml")); err == nil {
		if err := yaml.Unmarshal(b, &runtime); err != nil {
			return fmt.Errorf("reading old runtime settings: %w", err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(fromDir, "config", "vacancy-view.yaml")); err == nil {
		if err := yaml.Unmarshal(b, &view); err != nil {
			return fmt.Errorf("reading old vacancy view settings: %w", err)
		}
	}
	defaults := config.Defaults()
	weights := map[string]float64{}
	if q := runtime.QuickMatch; q.Version != 0 && len(q.CriterionWeights) > 0 {
		for old, name := range oldCriteria {
			weights[name] = q.CriterionWeights[old]
		}
	}
	return store.Update(func(c *config.Config) error {
		for name, w := range weights {
			if w != defaults.Weights[name] {
				c.Weights[name] = w
				report.SettingsCarried = append(report.SettingsCarried, fmt.Sprintf("weights.%s: %g", name, w))
			}
		}
		for cur, r := range view.ExchangeRates {
			if cur = strings.ToUpper(cur); r > 0 && r != defaults.Rates[cur] {
				c.Rates[cur] = r
				report.SettingsCarried = append(report.SettingsCarried, fmt.Sprintf("rates.%s: %g", cur, r))
			}
		}
		sort.Strings(report.SettingsCarried)
		return nil
	})
}

type oldFit struct {
	FitsYou struct {
		Status   string   `json:"status"`
		Score    *int     `json:"score"`
		RawScore *float64 `json:"rawScore"`
		Groups   []struct {
			Criterion string  `json:"criterion"`
			Utility   float64 `json:"utility"`
			Known     bool    `json:"known"`
			Weight    float64 `json:"weight"`
		} `json:"groups"`
	} `json:"fitsYou"`
}

type value struct {
	u     float64
	known bool
}

func (a value) same(b value) bool { return a.known == b.known && math.Abs(a.u-b.u) < 1e-9 }

func values(f fit.Fit) map[string]value {
	out := map[string]value{}
	for _, c := range f.Criteria {
		out[c.Name] = value{c.Utility, c.Known}
	}
	return out
}

type oldRow struct {
	profile                      string
	job                          int64
	userRev, profRev             int
	identity, hash, rec          string
	title, description, location string
	tags                         string
}

func compareFit(ctx context.Context, old *sql.DB, newDB *db.DB, cfg config.Config, report *importReport, dataDir string) error {
	var n int
	if err := old.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name='match_fit_results'`).Scan(&n); err != nil || n == 0 {
		return err
	}
	cmp := &report.Fit
	cmp.Found, cmp.ByCode, cmp.UnexplainedBy = true, map[string]int{}, map[string]int{}
	rules := fit.Rules{Weights: cfg.Weights, Rates: cfg.Rates}

	userRev := 0
	old.QueryRowContext(ctx, `SELECT revision FROM users LIMIT 1`).Scan(&userRev)
	profileRev := map[string]int{}
	identities := map[string]map[string]int{}
	var rows []oldRow
	rs, err := old.QueryContext(ctx, `
		SELECT f.profile_id, f.job_id, f.user_revision, f.profile_revision, f.rules_identity,
		       f.description_hash, f.record, COALESCE(p.revision, -1), j.title, j.description,
		       COALESCE(j.location, ''), COALESCE(j.tags, '')
		FROM match_fit_results f JOIN jobs j ON j.id = f.job_id
		LEFT JOIN user_profiles p ON p.id = f.profile_id ORDER BY f.profile_id, f.job_id`)
	if err != nil {
		return fmt.Errorf("reading old fit results: %w", err)
	}
	for rs.Next() {
		var r oldRow
		var rev int
		if err := rs.Scan(&r.profile, &r.job, &r.userRev, &r.profRev, &r.identity, &r.hash, &r.rec, &rev,
			&r.title, &r.description, &r.location, &r.tags); err != nil {
			rs.Close()
			return fmt.Errorf("reading old fit results: %w", err)
		}
		profileRev[r.profile] = rev
		if identities[r.profile] == nil {
			identities[r.profile] = map[string]int{}
		}
		identities[r.profile][r.identity]++
		rows = append(rows, r)
	}
	rs.Close()
	usual := map[string]string{}
	for p, ids := range identities {
		for id, c := range ids {
			if c > ids[usual[p]] || (c == ids[usual[p]] && id < usual[p]) {
				usual[p] = id
			}
		}
	}

	loaded, err := fit.LoadProfiles(ctx, newDB, rules)
	if err != nil {
		return err
	}
	me, err := candidate.LoadMe(ctx, newDB)
	if err != nil {
		return err
	}
	profiles, cat := map[int64]fit.ProfileView{}, catalog.New(newDB, nil)
	for _, p := range loaded {
		profiles[p.ID] = p.View
	}
	out := []string{"profile\tvacancy\told\tnew\tcriteria\tcode"}
	for _, r := range rows {
		sum := sha256.Sum256([]byte(r.description))
		if r.profRev != profileRev[r.profile] || r.userRev != userRev || r.hash != hex.EncodeToString(sum[:]) || r.identity != usual[r.profile] {
			cmp.NotCurrent++
			continue
		}
		pid, okP := report.profileIDs[r.profile]
		view, okG := profiles[pid]
		vid, merged := r.job, false
		raw, now, okV, err := fit.LoadVacancy(ctx, cat, r.job)
		if err == nil && !okV {
			if newDB.QueryRowContext(ctx, `SELECT new_id FROM vacancy_alias WHERE old_id=?`, r.job).Scan(&vid) == nil {
				merged = true
				raw, now, okV, err = fit.LoadVacancy(ctx, cat, vid)
			}
		}
		if err != nil {
			return err
		}
		var o oldFit
		if !okP || !okG || !okV || json.Unmarshal([]byte(r.rec), &o) != nil {
			cmp.NotComp++
			continue
		}
		var recJSON string
		newDB.QueryRowContext(ctx, `SELECT record FROM vacancy_reading WHERE vacancy_id=?`, vid).Scan(&recJSON)
		var rec reading.Record
		json.Unmarshal([]byte(recJSON), &rec)
		d := diffCase{ctx: ctx, old: old, row: r, merged: merged, me: me, v: raw, rules: rules,
			pv: view, folded: report.foldedLabels(pid), rec: rec}
		d.now = now
		got := fit.Score(d.pv, d.now, rules)
		oldState, oldScore := map[string]string{"scored": fit.Scored, "unscored": fit.NoGoals, "unavailable": fit.NoText}[o.FitsYou.Status], 0
		if o.FitsYou.Score != nil {
			oldScore = *o.FitsYou.Score
		} else if o.FitsYou.RawScore != nil {
			oldScore = int(math.Round(*o.FitsYou.RawScore * 100))
		}
		if oldState == got.State && (got.State != fit.Scored || oldScore == got.Score) {
			cmp.Equal++
			continue
		}
		d.oldV, d.newV = map[string]value{}, values(got)
		for _, g := range o.FitsYou.Groups {
			d.oldV[oldCriteria[g.Criterion]] = value{g.Utility, g.Known}
		}
		var explained, codes []string
		unexplained := false
		if oldState != got.State {
			code := d.stateCause(oldState, got.State)
			explained = append(explained, "state="+or(code, "?"))
			if code == "" {
				unexplained = true
				cmp.UnexplainedBy["state"]++
			} else {
				codes = append(codes, code)
			}
		} else {
			for _, name := range fit.Criteria {
				if d.oldV[name].same(d.newV[name]) {
					continue
				}
				code := d.explain(name)
				explained = append(explained, name+"="+or(code, "?"))
				if code == "" {
					unexplained = true
					cmp.UnexplainedBy[name]++
				} else if !slices.Contains(codes, code) {
					codes = append(codes, code)
				}
			}
		}
		summary := "unexplained"
		if len(explained) == 0 {
			weights := map[string]float64{}
			for _, g := range o.FitsYou.Groups {
				weights[oldCriteria[g.Criterion]] = g.Weight
			}
			for _, name := range fit.Criteria {
				if weights[name] != max(rules.Weights[name], 0) && !slices.Contains(codes, "W1") {
					codes = append(codes, "W1")
				}
			}
			explained = append(explained, "number="+or(strings.Join(codes, ""), "?"))
			if len(codes) == 0 {
				cmp.UnexplainedBy["number only"]++
				unexplained = true
			}
		}
		if unexplained {
			cmp.Unexplained++
		} else {
			cmp.Expected++
			for _, c := range codes {
				cmp.ByCode[c]++
			}
			summary = strings.Join(codes, "+")
		}
		oldText, newText := oldState, got.State
		if oldState == fit.Scored {
			oldText = fmt.Sprint(oldScore)
		}
		if got.State == fit.Scored {
			newText = fmt.Sprint(got.Score)
		}
		out = append(out, fmt.Sprintf("%d\t%d\t%s\t%s\t%s\t%s", pid, vid, oldText, newText, strings.Join(explained, ","), summary))
	}
	return os.WriteFile(filepath.Join(dataDir, "fit-comparison.tsv"), []byte(strings.Join(out, "\n")+"\n"), 0600)
}

type diffCase struct {
	ctx        context.Context
	old        *sql.DB
	row        oldRow
	merged     bool
	me         candidate.MeData
	pv         fit.ProfileView
	rules      fit.Rules
	v          reading.Vacancy
	now        reading.VacancyReading
	rec        reading.Record
	oldV, newV map[string]value
	folded     []string
}

func (d diffCase) with(r reading.VacancyReading, name string) value {
	return values(fit.Score(d.pv, r, d.rules))[name]
}

func (d diffCase) stateCause(oldState, newState string) string {
	switch {
	case d.merged:
		return "S2"
	case oldState == fit.NoText && newState != fit.NoText && metadataOnly(d.row):
		return "S1"
	}
	return ""
}

func (d diffCase) explain(name string) string {
	o, n := d.oldV[name], d.newV[name]
	geo := name == "location" || name == "remote_restriction" || name == "time_zone"
	remote, open, known := scope(d.now)
	for _, e := range expectedDifferences {
		applies := false
		switch e.code {
		case "E2":
			for _, l := range d.now.Locations {
				pl := places.Resolve(l)
				applies = applies || (geo && !n.known && !pl.Known() && len(pl.Cities) > 0)
			}
		case "E3":
			before := d.v
			before.Text = d.row.description
			r := reading.Merge(reading.FromCode(before), d.rec)
			applies = !reflect.DeepEqual(r, d.now) && d.with(r, name).same(o)
		case "E4":
			if (name == "location" || name == "remote_restriction") && !o.known && o.u == 0.5 && (d.v.Location != "" || len(d.v.Tags) > 0) {
				noBoard := d.v
				noBoard.Location, noBoard.Tags = "", nil
				applies = d.with(reading.FromCode(noBoard), name).same(o)
			}
		case "E5":
			applies = d.merged
		case "E6":
			eligible := false
			for _, a := range d.me.WorkAuthorizations {
				eligible = eligible || a.Status == "eligible"
			}
			applies = name == "remote_restriction" && o.known && o.u == 0 && !n.known && remote && !open && known && eligible
		case "E7":
			applies = name == "remote_restriction" && !o.known && n.known && n.u == 1 && open &&
				len(d.me.WorkAuthorizations) == 0 && d.me.CurrentLocation == nil && len(d.pv.Goals.PreferredLocations) == 0
		case "E8":
			for _, label := range d.folded {
				applies = applies || slices.Contains(foldedCriteria[label], name)
			}
		case "E9":
			applies = name == "time_zone" && o.known && !n.known && d.now.TimeZone != "" && places.ZoneBands(d.now.TimeZone) == nil
		case "E10":
			if h := d.now.Hours; name == "hours" && h != nil && h.Min > 0 {
				r, single := d.now, *h
				single.Min, r.Hours = 0, &single
				applies = d.with(r, name).same(o)
			}
		case "E11":
			if m := d.now.Money; name == "money" && !o.known && n.known && n.u == 1 && m != nil && m.Min > 0 {
				r, bottom := d.now, *m
				bottom.Max, r.Money = m.Min, &bottom
				applies = d.with(r, name).u < 1
			}
		case "E12":
			applies = name == "location" && !o.known && o.u == 0.5 && n.known
		case "E13":
			applies = !o.known && n.known && extractionFillsFactsGap(d.ctx, d.old, d.row.job, name)
		}
		if applies {
			return e.code
		}
	}
	return ""
}

func extractionFillsFactsGap(ctx context.Context, old *sql.DB, job int64, name string) bool {
	var extJSON, factsJSON string
	old.QueryRowContext(ctx, `SELECT record FROM match_vacancy_extractions WHERE job_id=? ORDER BY created_at DESC LIMIT 1`, job).Scan(&extJSON)
	old.QueryRowContext(ctx, `SELECT facts FROM match_job_facts WHERE job_id=?`, job).Scan(&factsJSON)
	var ext struct {
		Conditions map[string]*struct{ Value string } `json:"conditions"`
	}
	var facts struct {
		Compensation struct{ Cash json.RawMessage } `json:"compensation"`
		Geography    struct {
			HiringLocations []json.RawMessage `json:"hiringLocations"`
		} `json:"geography"`
		Conditions struct {
			WorkModes                   []json.RawMessage `json:"workModes"`
			Engagement, Hours, Timezone json.RawMessage
		} `json:"conditions"`
	}
	if json.Unmarshal([]byte(extJSON), &ext) != nil {
		return false
	}
	json.Unmarshal([]byte(factsJSON), &facts)
	has := func(r json.RawMessage) bool { return len(r) > 0 && string(r) != "null" }
	locations := len(facts.Geography.HiringLocations) > 0
	type pair struct {
		extKey  string
		inFacts bool
	}
	pairs := map[string][]pair{
		"money":              {{"compensation", has(facts.Compensation.Cash)}},
		"location":           {{"location", locations}},
		"remote_restriction": {{"location", locations}, {"workMode", len(facts.Conditions.WorkModes) > 0}},
		"time_zone":          {{"timezone", has(facts.Conditions.Timezone)}, {"location", locations}},
		"work_mode":          {{"workMode", len(facts.Conditions.WorkModes) > 0}},
		"engagement":         {{"engagement", has(facts.Conditions.Engagement)}},
		"hours":              {{"hours", has(facts.Conditions.Hours)}},
	}
	for _, p := range pairs[name] {
		if c := ext.Conditions[p.extKey]; c != nil && strings.TrimSpace(c.Value) != "" && !p.inFacts {
			return true
		}
	}
	return false
}

var foldedCriteria = map[string][]string{
	"work-mode limits": {"work_mode"}, "work arrangement": {"work_mode"},
	"geography current": {"location", "time_zone"}, "geography desired": {"location", "time_zone"},
	"relocation": {"location"}, "eligibility": {"remote_restriction"}, "sponsorship": {"remote_restriction"},
	"engagement preferred": {"engagement"},
}

func scope(r reading.VacancyReading) (remote, open, known bool) {
	remote = slices.Contains(r.WorkModes, "remote")
	open, known = remote, true
	for _, l := range r.Locations {
		pl := places.Resolve(l)
		open, known = open && pl.Worldwide, known && pl.Known()
	}
	return remote, open, known
}

func metadataOnly(r oldRow) bool {
	norm := func(s string) string {
		return strings.ToLower(strings.Join(strings.Fields(strings.NewReplacer(",", " ", ";", " ", ":", " ").Replace(s)), " "))
	}
	var tags []string
	json.Unmarshal([]byte(r.tags), &tags)
	for i := range tags {
		tags[i] = norm(tags[i])
	}
	desc, title, joined := norm(r.description), norm(r.title), strings.Join(slices.DeleteFunc(tags, func(s string) bool { return s == "" }), " ")
	return desc != "" && (desc == title || joined != "" && (desc == joined || title != "" && (desc == title+" "+joined || desc == joined+" "+title)))
}

func (r *importReport) foldedLabels(profileID int64) []string {
	var out []string
	for _, line := range r.LegacyFolded {
		for _, prefix := range []string{"My data (", fmt.Sprintf("profile %d (", profileID)} {
			if rest, ok := strings.CutPrefix(line, prefix); ok {
				out = append(out, rest[:max(strings.Index(rest, "): "), 0)])
			}
		}
	}
	return out
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func formatFitComparison(sb *strings.Builder, c fitComparison) {
	fmt.Fprintf(sb, "Fits you compared with the previous version:\n")
	if !c.Found {
		fmt.Fprintf(sb, "  no previous results\n")
		return
	}
	fmt.Fprintf(sb, "  equal: %d\n  different by expected difference: %d (a row may count under several codes)\n", c.Equal, c.Expected)
	for _, e := range expectedDifferences {
		if c.ByCode[e.code] > 0 {
			fmt.Fprintf(sb, "    %s %s: %d\n", e.code, e.text, c.ByCode[e.code])
		}
	}
	fmt.Fprintf(sb, "  different unexplained: %d\n", c.Unexplained)
	for _, name := range append([]string{"state", "number only"}, fit.Criteria...) {
		if c.UnexplainedBy[name] > 0 {
			fmt.Fprintf(sb, "    %s: %d\n", name, c.UnexplainedBy[name])
		}
	}
	fmt.Fprintf(sb, "  not comparable: %d\n  old results not current (not compared): %d\n", c.NotComp, c.NotCurrent)
	fmt.Fprintf(sb, "  rows of differences: fit-comparison.tsv\n")
}
