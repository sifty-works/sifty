// Package inbox handles the vacancy list, decisions, seen and delete endpoints.
package inbox

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/sifty-works/sifty-server/internal/apperr"
	"github.com/sifty-works/sifty-server/internal/candidate"
	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/listing"
)

// repoRow is the raw data loaded from a list query.
type repoRow struct {
	id             int64
	company        string
	title          string
	location       string
	publishedAt    string
	publishedBasis string
	industries     string
	unread         bool
	stage          string
	reason         string
	appliedAt      string
	appliedName    string
	expiresAt      string
}

const listCols = `v.id, v.company, v.title, v.location, v.published_at, v.published_basis,
    v.industries,
    v.seen_at='' AS unread, COALESCE(d.reason,'') AS reason,
    (SELECT applied_at FROM application WHERE vacancy=v.id ORDER BY applied_at DESC LIMIT 1),
    (SELECT profile_name FROM application WHERE vacancy=v.id ORDER BY applied_at DESC LIMIT 1),
    v.expires_at`

const orderNewest = `ORDER BY
    CASE WHEN published_basis != '' THEN 1 WHEN published_at != '' THEN 2 ELSE 3 END ASC,
    CASE WHEN published_at != '' THEN published_at ELSE '0000-00-00' END DESC,
    first_seen DESC, id DESC`

const orderDecided = `ORDER BY d.decided_at DESC, d.vacancy DESC`

// resolveProfile returns pid if it exists, otherwise 404.
// When pid is 0 the active profile from settings is used, falling back to the
// profile with the lowest id when no active profile is set.
func resolveProfile(ctx context.Context, database *db.DB, pid int64) (int64, error) {
	if pid == 0 {
		return candidate.ActiveProfileID(ctx, database)
	}
	var id int64
	err := database.QueryRowContext(ctx, `SELECT id FROM profile WHERE id=?`, pid).Scan(&id)
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

// IndustryCount is one entry in the per-industry count returned by the list.
type IndustryCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// SourceCount is one entry in the per-source count returned by the list.
type SourceCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// notExpiredSQL is a SQL predicate that holds when a vacancy has no expiry date
// or its expiry date has not yet passed. The single ? argument is the current
// time in RFC3339 format. Applied to Inbox, Shortlist and Dismissed but not Applied.
const notExpiredSQL = `(v.expires_at='' OR v.expires_at>?)`

// remoteSQL is a SQL predicate that holds when the vacancy location contains
// "remote", "anywhere", or "telecommute" as whole words after lightweight
// normalisation (lowercase, hyphens and middle-dots replaced with spaces).
// char(183) is U+00B7 MIDDLE DOT (·).
const remoteSQL = `(` +
	`instr(' '||lower(replace(replace(v.location,'-',' '),char(183),' '))||' ',' remote ')>0 OR ` +
	`instr(' '||lower(replace(replace(v.location,'-',' '),char(183),' '))||' ',' anywhere ')>0 OR ` +
	`instr(' '||lower(replace(replace(v.location,'-',' '),char(183),' '))||' ',' telecommute ')>0` +
	`)`

// leadershipClause returns the SQL predicate and bound args that hold when the
// vacancy title contains CTO, Head, Director, Manager, or Lead as whole words.
// Lead is excluded when "lead generation" also appears in the title.
// The title column of vacancy_fts holds pre-normalised text so padded phrase
// queries follow the same pattern as roleClause in the candidate package.
func leadershipClause() (string, []any) {
	mk := func(term string) string {
		norm := listing.FTSNorm(term)
		return `title: "` + strings.ReplaceAll(norm, `"`, `""`) + `"`
	}
	leadMatch := mk("lead")
	leadGenMatch := mk("lead generation")
	args := []any{mk("CTO"), mk("Head"), mk("Director"), mk("Manager"), leadMatch, leadGenMatch}
	sql := `(` +
		`v.id IN (SELECT rowid FROM vacancy_fts WHERE vacancy_fts MATCH ?) OR ` +
		`v.id IN (SELECT rowid FROM vacancy_fts WHERE vacancy_fts MATCH ?) OR ` +
		`v.id IN (SELECT rowid FROM vacancy_fts WHERE vacancy_fts MATCH ?) OR ` +
		`v.id IN (SELECT rowid FROM vacancy_fts WHERE vacancy_fts MATCH ?) OR ` +
		`(v.id IN (SELECT rowid FROM vacancy_fts WHERE vacancy_fts MATCH ?) AND ` +
		`v.id NOT IN (SELECT rowid FROM vacancy_fts WHERE vacancy_fts MATCH ?))` +
		`)`
	return sql, args
}

// filterClauses returns SQL fragments and bound args for the user-supplied
// Filters f. When skipSources is true the sources clause is omitted so the
// caller can count across all sources while still honouring every other filter.
func filterClauses(now time.Time, f Filters, skipSources bool) ([]string, []any) {
	var parts []string
	var args []any

	if f.Collected != 0 {
		cutoff := now.AddDate(0, 0, -f.Collected).UTC().Format(time.RFC3339)
		parts = append(parts, "v.first_seen>=?")
		args = append(args, cutoff)
	}

	if f.Remote {
		parts = append(parts, remoteSQL)
	}

	if f.Leadership {
		lSQL, lArgs := leadershipClause()
		parts = append(parts, lSQL)
		args = append(args, lArgs...)
	}

	if !skipSources && len(f.Sources) > 0 {
		ph := make([]string, len(f.Sources))
		for i, s := range f.Sources {
			ph[i] = "?"
			args = append(args, s)
		}
		parts = append(parts, "v.id IN (SELECT vacancy_id FROM vacancy_source WHERE source IN ("+strings.Join(ph, ",")+"))")
	}

	if f.Unread {
		parts = append(parts, "v.seen_at=''")
	}

	return parts, args
}

// scopeClause builds the JOIN, WHERE, and base arguments for a profile+stage
// list query. vis is applied to inbox listings only. When skipSources is true
// the sources clause in f is omitted (used by sourceCounts). industries filters
// the result to vacancies with at least one of the named industries (skipped when
// nil or empty). The expiry condition is added for all stages except applied.
func scopeClause(now time.Time, profileID int64, stage, q string, vis candidate.Filter, f Filters, industries []string, skipSources bool) (join, where string, args []any) {
	isInbox := stage == "inbox"
	low := strings.ToLower(strings.TrimSpace(q))

	if isInbox {
		join = `LEFT JOIN decision d ON d.vacancy=v.id AND d.profile=?`
		where = `d.vacancy IS NULL AND ` + notExcludedFor
		args = []any{profileID, profileID}
		if vis.SQL != "" {
			where += ` AND (` + vis.SQL + `)`
			args = append(args, vis.Args...)
		}
	} else {
		join = `JOIN decision d ON d.vacancy=v.id AND d.profile=? AND d.stage=?`
		where = notExcludedFor
		args = []any{profileID, stage, profileID}
	}

	if low != "" {
		if len([]rune(q)) >= 3 {
			normQ := strings.TrimSpace(listing.FTSNorm(q))
			if normQ != "" {
				where += ` AND (instr(lower(v.title),?)>0 OR instr(lower(v.company),?)>0 OR instr(lower(v.tags),?)>0 OR v.id IN (SELECT rowid FROM vacancy_fts WHERE vacancy_fts MATCH ?))`
				args = append(args, low, low, low, `text: "`+strings.ReplaceAll(normQ, `"`, `""`)+`"`)
			} else {
				where += ` AND (instr(lower(v.title),?)>0 OR instr(lower(v.company),?)>0 OR instr(lower(v.tags),?)>0)`
				args = append(args, low, low, low)
			}
		} else {
			where += ` AND (instr(lower(v.title),?)>0 OR instr(lower(v.company),?)>0 OR instr(lower(v.tags),?)>0)`
			args = append(args, low, low, low)
		}
	}

	if len(industries) > 0 {
		var iparts []string
		for _, ind := range industries {
			ind = strings.TrimSpace(ind)
			if ind == "" {
				continue
			}
			iparts = append(iparts, "EXISTS (SELECT 1 FROM json_each(v.industries) WHERE value=?)")
			args = append(args, ind)
		}
		if len(iparts) > 0 {
			where += ` AND (` + strings.Join(iparts, " OR ") + `)`
		}
	}

	fParts, fArgs := filterClauses(now, f, skipSources)
	for _, p := range fParts {
		where += ` AND ` + p
	}
	args = append(args, fArgs...)

	if stage != "applied" {
		where += ` AND ` + notExpiredSQL
		args = append(args, now.UTC().Format(time.RFC3339))
	}

	return join, where, args
}

// industryCounts returns the number of vacancies per industry for the given
// profile and stage, applying visibility (inbox only), exclusions, the query q
// and all Filters f — the same conditions as the row list — but NOT the
// industries filter itself. Only industries with at least one vacancy are
// returned, sorted alphabetically.
func industryCounts(ctx context.Context, database *db.DB, now time.Time, profileID int64, stage, q string, vis candidate.Filter, f Filters) ([]IndustryCount, error) {
	join, where, args := scopeClause(now, profileID, stage, q, vis, f, nil, false)

	rows, err := database.QueryContext(ctx, `
		SELECT j.value, COUNT(*)
		FROM vacancy v
		`+join+`
		JOIN json_each(v.industries) j
		WHERE j.value IS NOT NULL AND `+where+`
		GROUP BY j.value
		ORDER BY j.value`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("inbox: industry counts: %w", err)
	}
	defer rows.Close()
	var out []IndustryCount
	for rows.Next() {
		var ic IndustryCount
		if err := rows.Scan(&ic.Name, &ic.Count); err != nil {
			return nil, fmt.Errorf("inbox: industry counts scan: %w", err)
		}
		out = append(out, ic)
	}
	return out, rows.Err()
}

// sourceCounts returns the number of vacancies per source key for the given
// profile and stage, applying the same conditions as industryCounts but NOT
// the sources filter. Only sources with at least one vacancy are returned,
// sorted alphabetically by source key.
func sourceCounts(ctx context.Context, database *db.DB, now time.Time, profileID int64, stage, q string, vis candidate.Filter, industries []string, f Filters) ([]SourceCount, error) {
	join, where, args := scopeClause(now, profileID, stage, q, vis, f, industries, true)

	rows, err := database.QueryContext(ctx, `
		SELECT vs.source, COUNT(DISTINCT v.id)
		FROM vacancy v
		`+join+`
		JOIN vacancy_source vs ON vs.vacancy_id=v.id
		WHERE `+where+`
		GROUP BY vs.source
		ORDER BY vs.source`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("inbox: source counts: %w", err)
	}
	defer rows.Close()
	var out []SourceCount
	for rows.Next() {
		var sc SourceCount
		if err := rows.Scan(&sc.Name, &sc.Count); err != nil {
			return nil, fmt.Errorf("inbox: source counts scan: %w", err)
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// stageCounts returns the vacancy count per stage for a profile.
// Decided stages (shortlist, applied, dismissed) apply per-profile exclusions;
// expired vacancies are excluded from shortlist and dismissed but not from applied.
// The inbox count also applies the profile's visibility filter and the expiry condition.
func stageCounts(ctx context.Context, database *db.DB, now time.Time, profileID int64, vis candidate.Filter) (map[string]int, error) {
	out := map[string]int{}
	nowStr := now.UTC().Format(time.RFC3339)

	// Count decided stages: apply per-profile exclusions and exclude expired
	// vacancies from shortlist and dismissed (Applied is always shown).
	decidedRows, err := database.QueryContext(ctx, `
		SELECT d.stage, COUNT(*)
		FROM vacancy v
		JOIN decision d ON d.vacancy=v.id AND d.profile=?
		WHERE `+notExcludedFor+` AND (d.stage='applied' OR `+notExpiredSQL+`)
		GROUP BY d.stage`,
		profileID, profileID, nowStr)
	if err != nil {
		return nil, fmt.Errorf("inbox: counts decided: %w", err)
	}
	defer decidedRows.Close()
	for decidedRows.Next() {
		var s string
		var n int
		if err := decidedRows.Scan(&s, &n); err != nil {
			return nil, fmt.Errorf("inbox: counts scan: %w", err)
		}
		out[s] = n
	}
	if err := decidedRows.Err(); err != nil {
		return nil, fmt.Errorf("inbox: counts decided: %w", err)
	}

	// Count inbox: vacancies without a decision for this profile that fit the
	// search and have not expired.
	inboxArgs := append([]any{profileID, profileID}, vis.Args...)
	inboxArgs = append(inboxArgs, nowStr)
	var inboxCount int
	if err := database.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM vacancy v
		LEFT JOIN decision d ON d.vacancy=v.id AND d.profile=?
		WHERE d.vacancy IS NULL AND `+notExcludedFor+` AND (`+vis.SQL+`) AND `+notExpiredSQL,
		inboxArgs...).Scan(&inboxCount); err != nil {
		return nil, fmt.Errorf("inbox: counts inbox: %w", err)
	}
	out["inbox"] = inboxCount

	return out, nil
}

// listRows returns vacancy rows for the given profile and stage,
// optionally filtered by query q, Filters f and by industry names.
// vis is applied to inbox listings only.
func listRows(ctx context.Context, database *db.DB, now time.Time, profileID int64, stage, q string, vis candidate.Filter, f Filters, industries ...string) ([]repoRow, error) {
	var order string
	switch stage {
	case "inbox", "shortlist":
		order = orderNewest
	default:
		order = orderDecided
	}

	join, where, args := scopeClause(now, profileID, stage, q, vis, f, industries, false)

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
			&row.publishedAt, &row.publishedBasis,
			&row.industries,
			&unread, &row.reason,
			&appliedAt, &appliedName, &row.expiresAt,
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

// viewData holds the vacancy fields needed for the view endpoint that are
// not covered by the list row: the original URL, the apply address, the text
// state, the compressed body and the source keys.
type viewData struct {
	url       string
	applyURL  string
	textState string
	body      []byte
	sources   []string
}

// loadVacancyFull returns the stage, row data and view-only fields for the
// given (profile, vacancy) pair in one query. It answers NotFound when no
// vacancy row exists or the vacancy is hidden by an exclusion for this profile.
func loadVacancyFull(ctx context.Context, database *db.DB, profileID, vacancyID int64) (stage string, row repoRow, vd viewData, err error) {
	var unread int64
	var appliedAt, appliedName sql.NullString
	err = database.QueryRowContext(ctx, `
		SELECT COALESCE(d.stage,'inbox'),
		       v.id, v.company, v.title, v.location, v.published_at, v.published_basis,
		       v.industries, v.seen_at='' AS unread, COALESCE(d.reason,'') AS reason,
		       (SELECT applied_at FROM application WHERE vacancy=v.id ORDER BY applied_at DESC LIMIT 1),
		       (SELECT profile_name FROM application WHERE vacancy=v.id ORDER BY applied_at DESC LIMIT 1),
		       v.expires_at,
		       v.url, v.apply_url, v.text_state, v.body
		FROM vacancy v LEFT JOIN decision d ON d.vacancy=v.id AND d.profile=?
		WHERE v.id=? AND `+notExcludedFor,
		profileID, vacancyID, profileID).Scan(
		&stage,
		&row.id, &row.company, &row.title, &row.location, &row.publishedAt, &row.publishedBasis,
		&row.industries, &unread, &row.reason,
		&appliedAt, &appliedName,
		&row.expiresAt,
		&vd.url, &vd.applyURL, &vd.textState, &vd.body)
	if err == sql.ErrNoRows {
		return "", repoRow{}, viewData{}, apperr.NotFound{Message: "Role not found."}
	}
	if err != nil {
		return "", repoRow{}, viewData{}, fmt.Errorf("inbox: vacancy full: %w", err)
	}
	row.unread = unread != 0
	row.stage = stage
	row.appliedAt = appliedAt.String
	row.appliedName = appliedName.String

	rows2, err2 := database.QueryContext(ctx,
		`SELECT source FROM vacancy_source WHERE vacancy_id=? ORDER BY rowid`, vacancyID)
	if err2 != nil {
		return "", repoRow{}, viewData{}, fmt.Errorf("inbox: vacancy sources: %w", err2)
	}
	defer rows2.Close()
	for rows2.Next() {
		var src string
		if err2 := rows2.Scan(&src); err2 != nil {
			return "", repoRow{}, viewData{}, fmt.Errorf("inbox: vacancy sources scan: %w", err2)
		}
		vd.sources = append(vd.sources, src)
	}
	return stage, row, vd, rows2.Err()
}
