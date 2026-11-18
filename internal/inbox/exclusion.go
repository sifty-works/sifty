package inbox

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/sifty-works/sifty/internal/apperr"
	"github.com/sifty-works/sifty/internal/db"
	"github.com/sifty-works/sifty/internal/listing"
)

// notExcluded is a SQL predicate that holds when a vacancy row is not hidden
// by any all-profile exclusion. Used in list and count queries.
// Word exclusions match the company only when it is not the 'Unknown' placeholder
// (assigned before a vacancy's own page is read).
const notExcluded = `NOT EXISTS (
	SELECT 1 FROM exclusion e WHERE e.profile_id=0 AND (
		(e.kind='word' AND (instr(lower(title),e.norm)>0 OR (company!='Unknown' AND instr(lower(company),e.norm)>0)))
		OR (e.kind='company' AND (
			substr(identity,1,instr(identity,'|')-1)=e.norm
			OR (apply_url!='' AND instr(apply_url,'.greenhouse.io/'||lower(e.value)||'/jobs/')>0)
		))
	)
)`

type exclRow struct {
	id          int64
	kind, value string
}

// normFor returns the stored norm for a given kind and value.
func normFor(kind, value string) string {
	if kind == "company" {
		return strings.TrimSuffix(listing.Identity(value, ""), "|")
	}
	return strings.ToLower(strings.TrimSpace(value))
}

// Excluded reports whether a vacancy with the given title and company matches
// any all-profile exclusion. Called by collection before and after Detail.
// Word exclusions do not match the 'Unknown' placeholder company.
func Excluded(ctx context.Context, database *db.DB, title, company string) (bool, error) {
	companyForWord := strings.ToLower(company)
	if company == "Unknown" {
		companyForWord = ""
	}
	var n int
	err := database.QueryRowContext(ctx, `SELECT 1 FROM exclusion WHERE profile_id=0 AND (
		(kind='word' AND (instr(?,norm)>0 OR instr(?,norm)>0))
		OR (kind='company' AND norm=?)
	) LIMIT 1`,
		strings.ToLower(title), companyForWord,
		strings.TrimSuffix(listing.Identity(company, ""), "|"),
	).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func queryExclusions(ctx context.Context, database *db.DB) ([]exclRow, error) {
	rows, err := database.QueryContext(ctx,
		`SELECT id,kind,value FROM exclusion WHERE profile_id=0 ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("inbox: list exclusions: %w", err)
	}
	defer rows.Close()
	var out []exclRow
	for rows.Next() {
		var r exclRow
		if err := rows.Scan(&r.id, &r.kind, &r.value); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func addExclusion(ctx context.Context, database *db.DB, kind, value string) ([]exclRow, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, apperr.Invalid{Message: "Value must not be empty."}
	}
	if len([]rune(value)) > 200 {
		return nil, apperr.Invalid{Message: "Value must be at most 200 characters."}
	}
	if kind != "word" && kind != "company" {
		return nil, apperr.Invalid{Message: "Kind must be word or company."}
	}
	if kind == "company" && strings.EqualFold(value, "Unknown") {
		return nil, apperr.Invalid{Message: "This vacancy has no company name."}
	}
	_, err := database.ExecContext(ctx,
		`INSERT OR IGNORE INTO exclusion (kind,value,norm,profile_id,created) VALUES (?,?,?,0,?)`,
		kind, value, normFor(kind, value), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("inbox: add exclusion: %w", err)
	}
	return queryExclusions(ctx, database)
}

func removeExclusion(ctx context.Context, database *db.DB, id int64) ([]exclRow, error) {
	if _, err := database.ExecContext(ctx, `DELETE FROM exclusion WHERE id=?`, id); err != nil {
		return nil, fmt.Errorf("inbox: remove exclusion: %w", err)
	}
	return queryExclusions(ctx, database)
}

func previewCount(ctx context.Context, database *db.DB, kind, value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" || (kind != "word" && kind != "company") {
		return 0, nil
	}
	norm := normFor(kind, value)
	var n int
	err := database.QueryRowContext(ctx, `SELECT count(*) FROM vacancy WHERE
		((? ='word' AND (instr(lower(title),?)>0 OR (company!='Unknown' AND instr(lower(company),?)>0)))
		OR (? ='company' AND (substr(identity,1,instr(identity,'|')-1)=?
			OR (apply_url!='' AND instr(apply_url,'.greenhouse.io/'||lower(?)||'/jobs/')>0))))
		AND `+notExcluded,
		kind, norm, norm, kind, norm, value,
	).Scan(&n)
	return n, err
}

func exclResponse(rows []exclRow) map[string]any {
	list := make([]map[string]any, len(rows))
	for i, r := range rows {
		list[i] = map[string]any{"id": r.id, "kind": r.kind, "value": r.value, "scope": "All profiles"}
	}
	return map[string]any{"exclusions": list}
}
