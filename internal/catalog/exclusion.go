package catalog

import (
	"context"
	"fmt"

	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/listing"
)

const exclInner = `(e.kind='word' AND (instr(lower(title),e.norm)>0 OR (company!='' AND instr(lower(company),e.norm)>0)))
		OR (e.kind='company' AND (
			substr(identity,1,instr(identity,'|')-1)=e.norm
			OR (apply_url!='' AND instr(apply_url,'.greenhouse.io/'||lower(e.value)||'/jobs/')>0)
		))`

const NotExcluded = `NOT EXISTS (SELECT 1 FROM exclusion e WHERE e.profile_id=0 AND (` + exclInner + `))`

const NotExcludedFor = `NOT EXISTS (SELECT 1 FROM exclusion e WHERE (e.profile_id=0 OR e.profile_id=?) AND (` + exclInner + `))`

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
