package inbox

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sifty-works/sifty-server/internal/apperr"
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

// notExcluded is a SQL predicate that holds when a vacancy row is not hidden
// by any all-profile exclusion. Used in list and count queries.
const notExcluded = `NOT EXISTS (SELECT 1 FROM exclusion e WHERE e.profile_id=0 AND (` + exclInner + `))`

// notExcludedFor is a SQL predicate that holds when a vacancy row is not hidden
// by any exclusion that applies globally (profile_id=0) or to a specific profile.
// The first ? argument binds the profile id.
const notExcludedFor = `NOT EXISTS (SELECT 1 FROM exclusion e WHERE (e.profile_id=0 OR e.profile_id=?) AND (` + exclInner + `))`

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

// Excluded reports whether a vacancy with the given title, company and apply
// address matches any all-profile exclusion. Called by collection before and
// after Detail. Uses the same notExcluded predicate as the inbox list.
func Excluded(ctx context.Context, database *db.DB, title, company, applyURL string) (bool, error) {
	identity := listing.Identity(company, title)
	var n int
	err := database.QueryRowContext(ctx,
		`SELECT count(*) FROM (SELECT ? AS title, ? AS company, ? AS identity, ? AS apply_url) WHERE NOT `+notExcluded,
		title, company, identity, applyURL,
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("inbox: excluded: %w", err)
	}
	return n > 0, nil
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

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("inbox: preview: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var before int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM vacancy WHERE `+notExcluded).Scan(&before); err != nil {
		return 0, fmt.Errorf("inbox: preview: count before: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO exclusion (kind,value,norm,profile_id,created) VALUES (?,?,?,0,'preview')`,
		kind, value, norm); err != nil {
		return 0, fmt.Errorf("inbox: preview: insert: %w", err)
	}
	var after int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM vacancy WHERE `+notExcluded).Scan(&after); err != nil {
		return 0, fmt.Errorf("inbox: preview: count after: %w", err)
	}
	return before - after, nil
}

// ExclusionKind is the kind of an exclusion rule.
type ExclusionKind string

const (
	KindWord    ExclusionKind = "word"
	KindCompany ExclusionKind = "company"
)

// Exclusion is one exclusion rule as returned by the API.
type Exclusion struct {
	ID    int64         `json:"id"`
	Kind  ExclusionKind `json:"kind"`
	Value string        `json:"value"`
	Scope string        `json:"scope"`
}

// ExclusionsResp is the response body for GET/POST/DELETE /api/exclusions.
type ExclusionsResp struct {
	Exclusions []Exclusion `json:"exclusions"`
}

func toExclusionsResp(rows []exclRow) ExclusionsResp {
	list := make([]Exclusion, len(rows))
	for i, r := range rows {
		list[i] = Exclusion{ID: r.id, Kind: ExclusionKind(r.kind), Value: r.value, Scope: "All profiles"}
	}
	return ExclusionsResp{Exclusions: list}
}
