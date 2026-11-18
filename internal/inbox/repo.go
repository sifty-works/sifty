// Package inbox handles the vacancy list, decisions, seen and delete endpoints.
package inbox

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/sifty-works/sifty/internal/apperr"
	"github.com/sifty-works/sifty/internal/db"
)

// repoRow is the raw data loaded from a list query.
type repoRow struct {
	id             int64
	company        string
	title          string
	location       string
	publishedAt    string
	publishedBasis string
	firstSeen      string
	tags           string
	unread         bool
	stage          string
	reason         string
	decidedAt      string
	appliedAt      string
	appliedName    string
}

const listCols = `v.id, v.company, v.title, v.location, v.published_at, v.published_basis, v.first_seen, v.tags,
    v.seen_at='' AS unread, COALESCE(d.reason,'') AS reason, COALESCE(d.decided_at,'') AS decided_at,
    (SELECT applied_at FROM application WHERE vacancy=v.id ORDER BY applied_at DESC LIMIT 1),
    (SELECT profile_name FROM application WHERE vacancy=v.id ORDER BY applied_at DESC LIMIT 1)`

const orderNewest = `ORDER BY
    CASE WHEN published_basis != '' THEN 1 WHEN published_at != '' THEN 2 ELSE 3 END ASC,
    CASE WHEN published_at != '' THEN published_at ELSE '0000-00-00' END DESC,
    first_seen DESC, id DESC`

const orderDecided = `ORDER BY d.decided_at DESC, d.vacancy DESC`

// resolveProfile returns pid if it exists, otherwise 404.
// When pid is 0 the profile with the lowest id is used.
func resolveProfile(ctx context.Context, database *db.DB, pid int64) (int64, error) {
	var id int64
	var err error
	if pid == 0 {
		err = database.QueryRowContext(ctx, `SELECT id FROM profile ORDER BY id LIMIT 1`).Scan(&id)
	} else {
		err = database.QueryRowContext(ctx, `SELECT id FROM profile WHERE id=?`, pid).Scan(&id)
	}
	if err == sql.ErrNoRows {
		return 0, apperr.NotFound{Message: "Profile not found."}
	}
	if err != nil {
		return 0, fmt.Errorf("inbox: resolve profile: %w", err)
	}
	return id, nil
}

// resolveVacancy resolves id through vacancy_alias and returns the vacancy id.
// Returns NotFound when no matching vacancy exists.
func resolveVacancy(ctx context.Context, database *db.DB, id int64) (int64, error) {
	var realID int64
	err := database.QueryRowContext(ctx,
		`SELECT id FROM vacancy WHERE id=COALESCE((SELECT new_id FROM vacancy_alias WHERE old_id=?),?)`,
		id, id).Scan(&realID)
	if err == sql.ErrNoRows {
		return 0, apperr.NotFound{Message: "Role not found."}
	}
	if err != nil {
		return 0, fmt.Errorf("inbox: resolve vacancy: %w", err)
	}
	return realID, nil
}

// stageCounts returns the vacancy count per stage for a profile,
// applying all-profile exclusions but ignoring the search query.
func stageCounts(ctx context.Context, database *db.DB, profileID int64) (map[string]int, error) {
	rows, err := database.QueryContext(ctx, `
		SELECT CASE WHEN d.stage IS NULL THEN 'inbox' ELSE d.stage END, COUNT(*)
		FROM vacancy v
		LEFT JOIN decision d ON d.vacancy=v.id AND d.profile=?
		WHERE `+notExcluded+`
		GROUP BY CASE WHEN d.stage IS NULL THEN 'inbox' ELSE d.stage END`,
		profileID)
	if err != nil {
		return nil, fmt.Errorf("inbox: counts: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, fmt.Errorf("inbox: counts scan: %w", err)
		}
		out[s] = n
	}
	return out, rows.Err()
}

// listRows returns vacancy rows for the given profile and stage,
// optionally filtered by query q. When vacancyID is non-zero only that vacancy is returned.
func listRows(ctx context.Context, database *db.DB, profileID, vacancyID int64, stage, q string) ([]repoRow, error) {
	inbox := stage == "inbox"
	low := strings.ToLower(strings.TrimSpace(q))

	var join, stageWhere, order string
	var baseArgs []any
	if inbox {
		join = `LEFT JOIN decision d ON d.vacancy=v.id AND d.profile=?`
		stageWhere = `d.vacancy IS NULL AND `
		order = orderNewest
		baseArgs = []any{profileID}
	} else {
		join = `JOIN decision d ON d.vacancy=v.id AND d.profile=? AND d.stage=?`
		stageWhere = ""
		if stage == "shortlist" {
			order = orderNewest
		} else {
			order = orderDecided
		}
		baseArgs = []any{profileID, stage}
	}

	where := stageWhere + notExcluded
	args := baseArgs
	if vacancyID != 0 {
		where += ` AND v.id=?`
		args = append(args, vacancyID)
	}
	if low != "" {
		if len([]rune(q)) >= 3 {
			where += ` AND (instr(lower(v.title),?)>0 OR instr(lower(v.company),?)>0 OR instr(lower(v.tags),?)>0 OR v.id IN (SELECT rowid FROM vacancy_fts WHERE vacancy_fts MATCH ?))`
			args = append(args, low, low, low, ftsPhrase(q))
		} else {
			where += ` AND (instr(lower(v.title),?)>0 OR instr(lower(v.company),?)>0 OR instr(lower(v.tags),?)>0)`
			args = append(args, low, low, low)
		}
	}

	sqlRows, err := database.QueryContext(ctx,
		`SELECT `+listCols+` FROM vacancy v `+join+` WHERE `+where+` `+order,
		args...)
	if err != nil {
		return nil, fmt.Errorf("inbox: list: %w", err)
	}
	return scanListRows(stage, sqlRows)
}

func scanListRows(stage string, sqlRows *sql.Rows) ([]repoRow, error) {
	defer sqlRows.Close()
	var out []repoRow
	for sqlRows.Next() {
		var row repoRow
		var unread int64
		var appliedAt, appliedName sql.NullString
		if err := sqlRows.Scan(
			&row.id, &row.company, &row.title, &row.location,
			&row.publishedAt, &row.publishedBasis, &row.firstSeen, &row.tags,
			&unread, &row.reason, &row.decidedAt,
			&appliedAt, &appliedName,
		); err != nil {
			return nil, fmt.Errorf("inbox: scan: %w", err)
		}
		row.unread = unread != 0
		row.stage = stage
		row.appliedAt = appliedAt.String
		row.appliedName = appliedName.String
		out = append(out, row)
	}
	return out, sqlRows.Err()
}

// ftsPhrase wraps q as a quoted FTS5 phrase, doubling any embedded double quotes.
func ftsPhrase(q string) string {
	return `"` + strings.ReplaceAll(q, `"`, `""`) + `"`
}
