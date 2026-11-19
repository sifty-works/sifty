// Package candidate holds the profile search record and the visibility filter
// it produces. The filter is used by inbox to show only the vacancies that fit
// a profile's search.
package candidate

import (
	"strings"

	"github.com/sifty-works/sifty-server/internal/listing"
)

// ProfileData is the JSON record stored in the profile table's data column.
// It is the canonical serialisation of a profile; callers that read a profile
// row must unmarshal this type.
type ProfileData struct {
	Name               string        `json:"name"`
	Headline           string        `json:"headline,omitempty"`
	TargetRoles        []string      `json:"targetRoles,omitempty"`
	Keywords           []string      `json:"keywords,omitempty"`
	AllOf              []string      `json:"allOf,omitempty"`
	TargetIndustries   []string      `json:"targetIndustries,omitempty"`
	Compensation       *Compensation `json:"compensation,omitempty"`
	Engagement         []string      `json:"engagement,omitempty"` // "employment" | "contract"
	Hours              *Hours        `json:"hours,omitempty"`
	WorkModes          []string      `json:"workModes,omitempty"` // "remote" | "hybrid" | "onsite"
	PreferredLocations []Location    `json:"preferredLocations,omitempty"`
	Relocation         string        `json:"relocation,omitempty"` // "yes" | "no"
	TimeZones          []string      `json:"timeZones,omitempty"`
	OtherRequirements  string        `json:"otherRequirements,omitempty"`
}

// inheritedFields lists the ProfileData fields that a non-first profile inherits
// from the first profile when empty.
var inheritedFields = []string{
	"compensation",
	"engagement",
	"hours",
	"workModes",
	"preferredLocations",
	"targetIndustries",
}

// IsInheritable reports whether the named JSON field is inherited from the first
// profile when the profile has no value of its own.
func IsInheritable(field string) bool {
	for _, f := range inheritedFields {
		if f == field {
			return true
		}
	}
	return false
}

// Effective returns a copy of p with every empty inheritable field filled from
// base (the first profile). It is called at read time for every profile that is
// not the first profile.
func Effective(p, base ProfileData) ProfileData {
	out := p
	if out.Compensation == nil {
		out.Compensation = base.Compensation
	}
	if len(out.Engagement) == 0 {
		out.Engagement = base.Engagement
	}
	if out.Hours == nil {
		out.Hours = base.Hours
	}
	if len(out.WorkModes) == 0 {
		out.WorkModes = base.WorkModes
	}
	if len(out.PreferredLocations) == 0 {
		out.PreferredLocations = base.PreferredLocations
	}
	if len(out.TargetIndustries) == 0 {
		out.TargetIndustries = base.TargetIndustries
	}
	return out
}

// InheritedFields returns the set of field names that p inherits from base.
// A field is inherited when p's own value is empty and base has a non-empty value.
func InheritedFields(p, base ProfileData) []string {
	var out []string
	if p.Compensation == nil && base.Compensation != nil {
		out = append(out, "compensation")
	}
	if len(p.Engagement) == 0 && len(base.Engagement) > 0 {
		out = append(out, "engagement")
	}
	if p.Hours == nil && base.Hours != nil {
		out = append(out, "hours")
	}
	if len(p.WorkModes) == 0 && len(base.WorkModes) > 0 {
		out = append(out, "workModes")
	}
	if len(p.PreferredLocations) == 0 && len(base.PreferredLocations) > 0 {
		out = append(out, "preferredLocations")
	}
	if len(p.TargetIndustries) == 0 && len(base.TargetIndustries) > 0 {
		out = append(out, "targetIndustries")
	}
	return out
}

// Profile carries the search configuration of one profile.
type Profile struct {
	TargetRoles []string
	Keywords    []string
	AllOf       []string
}

// Filter is a SQL predicate fragment with its bound arguments.
// SQL is always non-empty: "1=0" when no vacancy can match.
// The vacancy table must be aliased "v" in the query that uses this filter.
type Filter struct {
	SQL  string
	Args []any
}

// Visibility returns a Filter selecting vacancies that fit p's search
// against a vacancy table aliased "v".
//
// Rules:
//   - A profile with neither target roles nor keywords produces Filter{SQL:"1=0"}.
//   - When both are present, a vacancy fits when any role matches the title AND
//     any keyword matches the title, tags or text.
//   - Every term in AllOf must also appear in the title, tags or text.
//   - Terms are matched as whole words or phrases, ignoring case and punctuation,
//     using the full-text index with padded normalised phrases.
func Visibility(p Profile) Filter {
	hasRoles := len(p.TargetRoles) > 0
	hasKeywords := len(p.Keywords) > 0

	if !hasRoles && !hasKeywords {
		return Filter{SQL: "1=0"}
	}

	var clauses []string
	var args []any

	if hasRoles {
		parts := make([]string, 0, len(p.TargetRoles))
		for _, r := range p.TargetRoles {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			part, roleArgs := roleClause(r)
			parts = append(parts, part)
			args = append(args, roleArgs...)
		}
		if len(parts) > 0 {
			clauses = append(clauses, "("+strings.Join(parts, " OR ")+")")
		}
	}

	if hasKeywords {
		parts := make([]string, 0, len(p.Keywords))
		for _, kw := range p.Keywords {
			kw = strings.TrimSpace(kw)
			if kw == "" {
				continue
			}
			part, kwArgs := termClause(kw)
			parts = append(parts, part)
			args = append(args, kwArgs...)
		}
		if len(parts) > 0 {
			clauses = append(clauses, "("+strings.Join(parts, " OR ")+")")
		}
	}

	for _, term := range p.AllOf {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		part, termArgs := termClause(term)
		clauses = append(clauses, part)
		args = append(args, termArgs...)
	}

	if len(clauses) == 0 {
		return Filter{SQL: "1=0"}
	}
	return Filter{SQL: strings.Join(clauses, " AND "), Args: args}
}

// roleClause returns the SQL fragment and arguments for matching a target role
// as a whole word in the vacancy title column of the full-text index.
// The vacancy table must be aliased "v".
func roleClause(role string) (string, []any) {
	norm := listing.FTSNorm(role)
	if strings.TrimSpace(norm) == "" {
		return "1=0", nil
	}
	matchArg := `title: "` + strings.ReplaceAll(norm, `"`, `""`) + `"`
	return `v.id IN (SELECT rowid FROM vacancy_fts WHERE vacancy_fts MATCH ?)`, []any{matchArg}
}

// termClause returns the SQL fragment and arguments for matching one term
// as a whole word or phrase in the vacancy title, tags or text columns of
// the full-text index. The vacancy table must be aliased "v".
func termClause(term string) (string, []any) {
	norm := listing.FTSNorm(term)
	if strings.TrimSpace(norm) == "" {
		return "1=0", nil
	}
	matchArg := `{title tags text}: "` + strings.ReplaceAll(norm, `"`, `""`) + `"`
	return `v.id IN (SELECT rowid FROM vacancy_fts WHERE vacancy_fts MATCH ?)`, []any{matchArg}
}
