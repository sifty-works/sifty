// Package inbox handles the vacancy list endpoint.
package inbox

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/sifty-works/sifty/internal/db"
)

// repoRow is the raw data loaded from the vacancy table.
type repoRow struct {
	id             int64
	company        string
	title          string
	location       string
	publishedAt    string // RFC3339 or ""
	publishedBasis string
	firstSeen      string // RFC3339
	tags           string // JSON array
}

// repo reads from the catalog tables.
type repo struct {
	db *db.DB
}

func newRepo(database *db.DB) *repo {
	return &repo{db: database}
}

const selectCols = `id, company, title, location, published_at, published_basis, first_seen, tags`

const orderNewest = `ORDER BY
	CASE WHEN published_basis != '' THEN 1 WHEN published_at != '' THEN 2 ELSE 3 END ASC,
	CASE WHEN published_at != '' THEN published_at ELSE '0000-00-00' END DESC,
	first_seen DESC,
	id DESC`

// count returns the total number of vacancies.
func (r *repo) count(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM vacancy`).Scan(&n); err != nil {
		return 0, fmt.Errorf("inbox: count: %w", err)
	}
	return n, nil
}

// all returns all rows in Newest order, without body.
func (r *repo) all(ctx context.Context) ([]repoRow, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+selectCols+` FROM vacancy `+orderNewest)
	if err != nil {
		return nil, fmt.Errorf("inbox: all: %w", err)
	}
	return scanRows(rows)
}

// searchMatches returns rows matching q. Title, company and tags are always
// searched with LIKE. When q is at least 3 characters the vacancy body is
// also searched via the FTS trigram index.
func (r *repo) searchMatches(ctx context.Context, q string) ([]repoRow, error) {
	low := strings.ToLower(q)
	var sqlRows *sql.Rows
	var err error
	if len([]rune(q)) >= 3 {
		fts := ftsPhrase(q)
		sqlRows, err = r.db.QueryContext(ctx, `
			SELECT `+selectCols+` FROM vacancy
			WHERE instr(lower(title), ?) > 0 OR instr(lower(company), ?) > 0 OR instr(lower(tags), ?) > 0
			   OR id IN (SELECT rowid FROM vacancy_fts WHERE vacancy_fts MATCH ?)
			`+orderNewest, low, low, low, fts)
	} else {
		sqlRows, err = r.db.QueryContext(ctx, `
			SELECT `+selectCols+` FROM vacancy
			WHERE instr(lower(title), ?) > 0 OR instr(lower(company), ?) > 0 OR instr(lower(tags), ?) > 0
			`+orderNewest, low, low, low)
	}
	if err != nil {
		return nil, fmt.Errorf("inbox: searchMatches: %w", err)
	}
	return scanRows(sqlRows)
}

// ftsPhrase wraps q as a quoted FTS5 phrase, doubling any embedded double quotes.
func ftsPhrase(q string) string {
	escaped := strings.ReplaceAll(q, `"`, `""`)
	return `"` + escaped + `"`
}

func scanRows(sqlRows *sql.Rows) ([]repoRow, error) {
	defer sqlRows.Close()
	var out []repoRow
	for sqlRows.Next() {
		var row repoRow
		if err := sqlRows.Scan(
			&row.id, &row.company, &row.title, &row.location,
			&row.publishedAt, &row.publishedBasis, &row.firstSeen, &row.tags,
		); err != nil {
			return nil, fmt.Errorf("inbox: scan: %w", err)
		}
		out = append(out, row)
	}
	return out, sqlRows.Err()
}
