package skills

import (
	"testing"

	"github.com/sifty-works/sifty-server/internal/candidate"
	"github.com/sifty-works/sifty-server/internal/reading"
)

func idx(haves, absents []string) *Index {
	var skills []candidate.Skill
	for _, h := range haves {
		skills = append(skills, candidate.Skill{Name: h})
	}
	var absent []candidate.AbsentSkill
	for _, a := range absents {
		absent = append(absent, candidate.AbsentSkill{Name: a})
	}
	return NewIndex(skills, absent)
}

func req(text, kind string, alts ...string) reading.Requirement {
	return reading.Requirement{Text: text, Kind: kind, Alts: alts}
}

func TestDecide_technologyContextMakesAmbiguousFit(t *testing.T) {
	i := idx([]string{"Go"}, nil)
	r := req("Go programming language", "skill")
	if got := Decide(r, i, nil); got != "✓" {
		t.Fatalf("want ✓, got %q", got)
	}
}

func TestDecide_ambiguousTermWithoutContextDoesNotMatch(t *testing.T) {
	i := idx([]string{"Go"}, nil)
	r := req("go to market", "skill")
	got := Decide(r, i, nil)
	if got == "✓" {
		t.Fatalf("go-to-market should not match Go skill, got %q", got)
	}
}

func TestDecide_alternativesMatchAnyHeld(t *testing.T) {
	i := idx([]string{"Python"}, nil)
	r := req("Kotlin, Python or Go", "skill", "Kotlin", "Python", "Go")
	if got := Decide(r, i, nil); got != "✓" {
		t.Fatalf("want ✓ with Python in alts, got %q", got)
	}
}

func TestDecide_alternativesNoneHeld(t *testing.T) {
	i := idx([]string{"Rust"}, nil)
	r := req("Kotlin, Python or Go", "skill", "Kotlin", "Python", "Go")
	got := Decide(r, i, nil)
	if got == "✓" {
		t.Fatalf("Rust not in alts, want not ✓, got %q", got)
	}
}

func TestDecide_unknownWordsInRequirementAreUnclear(t *testing.T) {
	i := idx([]string{"Go"}, nil)
	r := req("Go and Zoflux expertise", "skill")
	if got := Decide(r, i, nil); got != "?" {
		t.Fatalf("unknown word Zoflux should give ?, got %q", got)
	}
}

func TestDecide_absentSkillGivesNo(t *testing.T) {
	i := idx(nil, []string{"Python"})
	r := req("Python", "skill")
	if got := Decide(r, i, nil); got != "✕" {
		t.Fatalf("absent Python should give ✕, got %q", got)
	}
}

func TestDecide_confirmedHaveGivesYes(t *testing.T) {
	i := idx(nil, nil)
	r := req("security clearance", "skill")
	facts := []candidate.ConfirmedFact{{Text: "security clearance", Have: true}}
	if got := Decide(r, i, facts); got != "✓" {
		t.Fatalf("confirmed fact should give ✓, got %q", got)
	}
}

func TestDecide_confirmedHaveNotGivesNo(t *testing.T) {
	i := idx(nil, nil)
	r := req("driving licence", "skill")
	facts := []candidate.ConfirmedFact{{Text: "driving licence", Have: false}}
	if got := Decide(r, i, facts); got != "✕" {
		t.Fatalf("confirmed not-have should give ✕, got %q", got)
	}
}

func TestDecide_experienceKindIsAlwaysUnclear(t *testing.T) {
	i := idx([]string{"Go"}, nil)
	r := req("5+ years backend", "experience")
	if got := Decide(r, i, nil); got != "?" {
		t.Fatalf("experience kind should always give ?, got %q", got)
	}
}

func TestDecide_plainTechSkillHeld(t *testing.T) {
	i := idx([]string{"PostgreSQL"}, nil)
	r := req("PostgreSQL", "skill")
	if got := Decide(r, i, nil); got != "✓" {
		t.Fatalf("want ✓, got %q", got)
	}
}

func TestDecide_plainTechSkillNotHeld(t *testing.T) {
	i := idx([]string{"MySQL"}, nil)
	r := req("PostgreSQL", "skill")
	got := Decide(r, i, nil)
	if got == "✓" {
		t.Fatalf("MySQL should not match PostgreSQL, got %q", got)
	}
}

func TestDecide_multipleSkillsAllHeld(t *testing.T) {
	i := idx([]string{"Docker", "Kubernetes"}, nil)
	r := req("Docker and Kubernetes", "skill")
	if got := Decide(r, i, nil); got != "✓" {
		t.Fatalf("want ✓ when all skills held, got %q", got)
	}
}

func TestDecide_multipleSkillsPartialIsUnclear(t *testing.T) {
	i := idx([]string{"Docker"}, nil)
	r := req("Docker and Kubernetes", "skill")
	got := Decide(r, i, nil)
	if got == "✓" {
		t.Fatalf("only Docker held, should not give ✓, got %q", got)
	}
}

func TestDecide_tokenizePreservesCSharp(t *testing.T) {
	i := idx([]string{"C#"}, nil)
	r := req("C#", "skill")
	if got := Decide(r, i, nil); got != "✓" {
		t.Fatalf("C# should match C# skill, got %q", got)
	}
}

func TestDecide_tokenizePreservesCPlusPlus(t *testing.T) {
	i := idx([]string{"C++"}, nil)
	r := req("C++", "skill")
	if got := Decide(r, i, nil); got != "✓" {
		t.Fatalf("C++ should match C++ skill, got %q", got)
	}
}

func TestDecide_tokenizePreservesNodejs(t *testing.T) {
	i := idx([]string{"Node.js"}, nil)
	r := req("Node.js", "skill")
	if got := Decide(r, i, nil); got != "✓" {
		t.Fatalf("Node.js should match Node.js skill, got %q", got)
	}
}

func TestDecide_tokenizePreservesDotNet(t *testing.T) {
	i := idx([]string{".NET"}, nil)
	r := req(".NET", "skill")
	if got := Decide(r, i, nil); got != "✓" {
		t.Fatalf(".NET should match .NET skill, got %q", got)
	}
}

func TestDecide_hyphenJoinedAmbiguousTermNotMatched(t *testing.T) {
	i := idx([]string{"Go"}, nil)
	r := req("high-Go system", "skill")
	got := Decide(r, i, nil)
	if got == "✓" {
		t.Fatalf("hyphen-joined ambiguous term should not match, got %q", got)
	}
}

func TestNewIndex_skillNameWithParentheses(t *testing.T) {
	i := idx([]string{"Python (3.x)"}, nil)
	r := req("Python", "skill")
	if got := Decide(r, i, nil); got != "✓" {
		t.Fatalf("skill with paren suffix should still match, got %q", got)
	}
}

func TestDecide_orTokenInTextMeansAnyOf(t *testing.T) {
	i := idx([]string{"Python"}, nil)
	r := req("Go or Python", "skill")
	if got := Decide(r, i, nil); got != "✓" {
		t.Fatalf("or in text: Python held should give ✓, got %q", got)
	}
}

func TestDecide_genericOnlyTextIsUnclear(t *testing.T) {
	i := idx(nil, nil)
	r := req("strong experience required", "skill")
	if got := Decide(r, i, nil); got != "?" {
		t.Fatalf("generic-only text should give ?, got %q", got)
	}
}

func TestDecide_genericQualifierDoesNotBlockNonAmbiguousSkill(t *testing.T) {
	i := idx([]string{"Go"}, nil)
	r := req("Golang expertise", "skill")
	if got := Decide(r, i, nil); got != "✓" {
		t.Fatalf("want ✓ with generic qualifier, got %q", got)
	}
}

func TestDecide_proficiencyInContextMakesAmbiguousTermAccepted(t *testing.T) {
	i := idx([]string{"Go"}, nil)
	r := req("Advanced proficiency in Go", "skill")
	if got := Decide(r, i, nil); got != "✓" {
		t.Fatalf("want ✓ when proficiency precedes ambiguous term, got %q", got)
	}
}

func TestDecide_nonPrefixBlocksTermMatch(t *testing.T) {
	i := idx([]string{"EVM"}, nil)
	r := req("non-EVM chain experience", "skill")
	got := Decide(r, i, nil)
	if got == "✓" {
		t.Fatalf("non- prefix should block match, got %q", got)
	}
}
