package catalog

import (
	"context"
	"fmt"

	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/listing"
)

// exclInner is the kind/value matching part of an exclusion predicate.
// It references columns title, company, identity, apply_url from the vacancy table.
// Word exclusions do not match a vacancy whose company column is empty
// (the placeholder used before a vacancy's own page is read).
const exclInner = `(e.kind='word' AND (instr(lower(title),e.norm)>0 OR (company!='' AND instr(lower(company),e.norm)>0)))
		OR (e.kind='company' AND (
			substr(identity,1,instr(identity,'|')-1)=e.norm
			OR (apply_url!='' AND instr(apply_url,'.greenhouse.io/'||lower(e.value)||'/jobs/')>0)
		))`

// NotExcluded is a SQL predicate that holds when a vacancy row is not hidden
// by any all-profile exclusion. Used in list and count queries.
// The vacancy table must supply title, company, identity and apply_url columns.
const NotExcluded = `NOT EXISTS (SELECT 1 FROM exclusion e WHERE e.profile_id=0 AND (` + exclInner + `))`

// NotExcludedFor is a SQL predicate that holds when a vacancy row is not hidden
// by any exclusion that applies globally (profile_id=0) or to a specific profile.
// The first ? argument binds the profile id. The vacancy table must supply
// title, company, identity and apply_url columns.
const NotExcludedFor = `NOT EXISTS (SELECT 1 FROM exclusion e WHERE (e.profile_id=0 OR e.profile_id=?) AND (` + exclInner + `))`

// Excluded reports whether a vacancy with the given title, company and apply
// address matches any all-profile exclusion. Called by collection before and
// after Detail. Uses the same NotExcluded predicate as the inbox list.
func Excluded(ctx context.Context, database *db.DB, title, company, applyURL string) (bool, error) {
	identity := listing.Identity(company, title)
	var n int
	err := database.QueryRowContext(ctx,
		`SELECT count(*) FROM (SELECT ? AS title, ? AS company, ? AS identity, ? AS apply_url) WHERE NOT `+NotExcluded,
		title, company, identity, applyURL,
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("catalog: excluded: %w", err)
	}
	return n > 0, nil
}
