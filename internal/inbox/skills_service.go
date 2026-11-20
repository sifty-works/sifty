package inbox

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/sifty-works/sifty-server/internal/apperr"
	"github.com/sifty-works/sifty-server/internal/candidate"
	"github.com/sifty-works/sifty-server/internal/catalog"
	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/fit"
	"github.com/sifty-works/sifty-server/internal/skills"
)

type skillsResult struct {
	notice string
	added  bool
}

func applySkillsDecision(ctx context.Context, database *db.DB, vacancyID int64, reqIndex int, have bool, chosenSkill string, r fit.Rules) (skillsResult, error) {
	_, vr, _, ok, err := fit.LoadVacancy(ctx, catalog.New(database, nil), vacancyID)
	if err != nil {
		return skillsResult{}, err
	}
	if !ok {
		return skillsResult{}, apperr.NotFound{Message: "Role not found."}
	}
	if reqIndex < 0 || reqIndex >= len(vr.Requirements) {
		return skillsResult{}, apperr.Invalid{Message: "Requirement index out of range."}
	}
	req := vr.Requirements[reqIndex]
	named := skills.NamedSkillsFor(req)
	toAct := named
	if chosenSkill != "" {
		toAct = []string{chosenSkill}
	}
	me, err := candidate.LoadMe(ctx, database)
	if err != nil {
		return skillsResult{}, err
	}
	var newSkills, newConfirmed []string
	eqName := func(n string) func(candidate.Skill) bool {
		return func(s candidate.Skill) bool { return strings.EqualFold(s.Name, n) }
	}
	eqAbsent := func(n string) func(candidate.AbsentSkill) bool {
		return func(a candidate.AbsentSkill) bool { return strings.EqualFold(a.Name, n) }
	}
	addConfirmed := func() {
		eqText := func(f candidate.ConfirmedFact) bool { return f.Text == req.Text }
		if !slices.ContainsFunc(me.ConfirmedFacts, eqText) {
			me.ConfirmedFacts = append(me.ConfirmedFacts, candidate.ConfirmedFact{Text: req.Text, Have: have})
			newConfirmed = append(newConfirmed, req.Text)
		}
	}
	if len(toAct) > 0 {
		for _, name := range toAct {
			if have {
				if !slices.ContainsFunc(me.Skills, eqName(name)) {
					me.Skills = append(me.Skills, candidate.Skill{Name: name, Level: "Proficient"})
					newSkills = append(newSkills, name)
				}
				me.AbsentSkills = slices.DeleteFunc(me.AbsentSkills, eqAbsent(name))
			} else {
				if !slices.ContainsFunc(me.AbsentSkills, eqAbsent(name)) {
					me.AbsentSkills = append(me.AbsentSkills, candidate.AbsentSkill{Name: name})
					newSkills = append(newSkills, name)
				}
				me.Skills = slices.DeleteFunc(me.Skills, eqName(name))
			}
		}
		if chosenSkill == "" || len(named) <= 1 {
			upIdx := skills.NewIndex(me.Skills, me.AbsentSkills)
			want := map[bool]string{true: "✓", false: "✕"}[have]
			if skills.Decide(req, upIdx, me.ConfirmedFacts) != want {
				addConfirmed()
			}
		}
	} else {
		addConfirmed()
	}
	if len(newSkills)+len(newConfirmed) > 0 {
		if err := candidate.SaveMe(ctx, database, me); err != nil {
			return skillsResult{}, err
		}
		if err := fit.RecomputeVacancy(ctx, database, vacancyID, r); err != nil {
			return skillsResult{}, err
		}
	}
	return skillsResult{notice: buildNotice(newSkills, newConfirmed, have), added: len(newSkills)+len(newConfirmed) > 0}, nil
}

func buildNotice(skillItems, confirmedItems []string, have bool) string {
	if len(skillItems)+len(confirmedItems) == 0 {
		return "Nothing new to add; your data is already up to date."
	}
	var parts []string
	for _, item := range skillItems {
		if have {
			parts = append(parts, fmt.Sprintf("Added %q to your skills (level Proficient; change in your profile).", item))
		} else {
			parts = append(parts, fmt.Sprintf("Added %q to skills you do not have (change in your profile).", item))
		}
	}
	for _, item := range confirmedItems {
		parts = append(parts, fmt.Sprintf("Added %q to Confirmed by you (change in your profile).", item))
	}
	return strings.Join(parts, " ")
}
