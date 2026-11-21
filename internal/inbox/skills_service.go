package inbox

import (
	"context"
	"fmt"
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
	me, err := candidate.LoadMe(ctx, database)
	if err != nil {
		return skillsResult{}, err
	}
	ans := skills.Answer(vr.Requirements[reqIndex], have, chosenSkill, me)
	if len(ans.AddedSkills)+len(ans.AddedConfirmed) > 0 {
		if err := candidate.SaveMe(ctx, database, ans.Me); err != nil {
			return skillsResult{}, err
		}
		if err := fit.RecomputeVacancy(ctx, database, vacancyID, r); err != nil {
			return skillsResult{}, err
		}
	}
	return skillsResult{notice: buildNotice(ans.AddedSkills, ans.AddedConfirmed, have), added: len(ans.AddedSkills)+len(ans.AddedConfirmed) > 0}, nil
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
