// Package inbox handles the vacancy list, decisions, seen and delete endpoints.
package inbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sifty-works/sifty-server/internal/apperr"
	"github.com/sifty-works/sifty-server/internal/candidate"
	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/listing"
)

// resolveProfile returns pid if it exists, otherwise 404.
// When pid is 0 the active profile from settings is used, falling back to the
// profile with the lowest id when no active profile is set.

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
	industries     string
	unread         bool
	stage          string
	reason         string
	decidedAt      string
	appliedAt      string
	appliedName    string
	expiresAt      string
}

const listCols = `v.id, v.company, v.title, v.location, v.published_at, v.published_basis, v.first_seen, v.tags,
    v.industries,
    v.seen_at='' AS unread, COALESCE(d.reason,'') AS reason, COALESCE(d.decided_at,'') AS decided_at,
    (SELECT applied_at FROM application WHERE vacancy=v.id ORDER BY applied_at DESC LIMIT 1),
    (SELECT profile_name FROM application WHERE vacancy=v.id ORDER BY applied_at DESC LIMIT 1),
    v.expires_at`

const orderNewest = `ORDER BY
    CASE WHEN published_basis != '' THEN 1 WHEN published_at != '' THEN 2 ELSE 3 END ASC,
    CASE WHEN published_at != '' THEN published_at ELSE '0000-00-00' END DESC,
    first_seen DESC, id DESC`

const orderDecided = `ORDER BY d.decided_at DESC, d.vacancy DESC`

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

// loadProfileSearch reads the search and industry fields of a profile from the
// database. vocabNames restricts target industries to known names when non-nil;
// nil means no restriction (used by callers that have no vocabulary configured).
func loadProfileSearch(ctx context.Context, database *db.DB, profileID int64, vocabNames []string) (candidate.Profile, error) {
	var dataJSON string
	err := database.QueryRowContext(ctx, `SELECT data FROM profile WHERE id=?`, profileID).Scan(&dataJSON)
	if err == sql.ErrNoRows {
		return candidate.Profile{}, nil
	}
	if err != nil {
		return candidate.Profile{}, fmt.Errorf("inbox: load profile search: %w", err)
	}
	var pd candidate.ProfileData
	if err := json.Unmarshal([]byte(dataJSON), &pd); err != nil {
		return candidate.Profile{}, nil
	}

	// For target industries, inherit from profile 1 when the profile's own list
	// is empty and the profile is not itself the first profile.
	targetIndustries := pd.TargetIndustries
	if len(targetIndustries) == 0 && profileID != 1 {
		var baseJSON string
		if err2 := database.QueryRowContext(ctx, `SELECT data FROM profile WHERE id=1`).Scan(&baseJSON); err2 == nil {
			var base candidate.ProfileData
			if json.Unmarshal([]byte(baseJSON), &base) == nil {
				targetIndustries = base.TargetIndustries
			}
		}
	}

	// Restrict to current vocabulary names when a vocabulary is configured.
	if vocabNames != nil && len(targetIndustries) > 0 {
		allowed := make(map[string]bool, len(vocabNames))
		for _, n := range vocabNames {
			allowed[n] = true
		}
		filtered := make([]string, 0, len(targetIndustries))
		for _, n := range targetIndustries {
			if allowed[n] {
				filtered = append(filtered, n)
			}
		}
		targetIndustries = filtered
	}

	return candidate.Profile{
		TargetRoles:      pd.TargetRoles,
		Keywords:         pd.Keywords,
		AllOf:            pd.AllOf,
		TargetIndustries: targetIndustries,
	}, nil
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

	if f.HideExpired {
		parts = append(parts, "(v.expires_at='' OR v.expires_at>?)")
		args = append(args, now.UTC().Format(time.RFC3339))
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

// industryCounts returns the number of vacancies per industry for the given
// profile and stage, applying visibility (inbox only), exclusions, the query q
// and all Filters f — the same conditions as the row list — but NOT the
// industries filter itself. Only industries with at least one vacancy are
// returned, sorted alphabetically.
func industryCounts(ctx context.Context, database *db.DB, now time.Time, profileID int64, stage, q string, vis candidate.Filter, f Filters) ([]IndustryCount, error) {
	isInbox := stage == "inbox"
	low := strings.ToLower(strings.TrimSpace(q))

	var join, where string
	var args []any
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

	fParts, fArgs := filterClauses(now, f, false)
	for _, p := range fParts {
		where += ` AND ` + p
	}
	args = append(args, fArgs...)

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
	isInbox := stage == "inbox"
	low := strings.ToLower(strings.TrimSpace(q))

	var join, where string
	var args []any
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

	fParts, fArgs := filterClauses(now, f, true) // skip sources
	for _, p := range fParts {
		where += ` AND ` + p
	}
	args = append(args, fArgs...)

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
// Decided stages (shortlist, applied, dismissed) apply per-profile exclusions.
// The inbox count also applies the profile's visibility filter.
func stageCounts(ctx context.Context, database *db.DB, profileID int64, vis candidate.Filter) (map[string]int, error) {
	out := map[string]int{}

	// Count decided stages: apply per-profile exclusions only.
	decidedRows, err := database.QueryContext(ctx, `
		SELECT d.stage, COUNT(*)
		FROM vacancy v
		JOIN decision d ON d.vacancy=v.id AND d.profile=?
		WHERE `+notExcludedFor+`
		GROUP BY d.stage`,
		profileID, profileID)
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

	// Count inbox: vacancies without a decision for this profile that fit the search.
	inboxArgs := append([]any{profileID, profileID}, vis.Args...)
	var inboxCount int
	if err := database.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM vacancy v
		LEFT JOIN decision d ON d.vacancy=v.id AND d.profile=?
		WHERE d.vacancy IS NULL AND `+notExcludedFor+` AND (`+vis.SQL+`)`,
		inboxArgs...).Scan(&inboxCount); err != nil {
		return nil, fmt.Errorf("inbox: counts inbox: %w", err)
	}
	out["inbox"] = inboxCount

	return out, nil
}

// listRows returns vacancy rows for the given profile and stage,
// optionally filtered by query q, Filters f and by industry names.
// When vacancyID is non-zero only that vacancy is returned and f is ignored.
// vis is applied to inbox listings only (stage == "inbox" and vacancyID == 0);
// it is ignored for decided stages and for single-vacancy fetches.
func listRows(ctx context.Context, database *db.DB, now time.Time, profileID, vacancyID int64, stage, q string, vis candidate.Filter, f Filters, industries ...string) ([]repoRow, error) {
	isInbox := stage == "inbox"
	low := strings.ToLower(strings.TrimSpace(q))

	var join, stageWhere, order string
	var baseArgs []any
	if isInbox {
		join = `LEFT JOIN decision d ON d.vacancy=v.id AND d.profile=?`
		stageWhere = `d.vacancy IS NULL AND `
		order = orderNewest
		baseArgs = []any{profileID, profileID} // join arg, then notExcludedFor arg
	} else {
		join = `JOIN decision d ON d.vacancy=v.id AND d.profile=? AND d.stage=?`
		stageWhere = ""
		if stage == "shortlist" {
			order = orderNewest
		} else {
			order = orderDecided
		}
		baseArgs = []any{profileID, stage, profileID} // join args, then notExcludedFor arg
	}

	where := stageWhere + notExcludedFor
	args := baseArgs

	// Apply visibility filter to inbox listings only (not to single-vacancy fetches).
	if isInbox && vacancyID == 0 && vis.SQL != "" {
		where += ` AND (` + vis.SQL + `)`
		args = append(args, vis.Args...)
	}

	if vacancyID != 0 {
		where += ` AND v.id=?`
		args = append(args, vacancyID)
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

	// Apply industries filter: show only vacancies that have at least one of the
	// given industry names. Applied to all stages.
	if vacancyID == 0 && len(industries) > 0 {
		var parts []string
		for _, ind := range industries {
			ind = strings.TrimSpace(ind)
			if ind == "" {
				continue
			}
			parts = append(parts, "EXISTS (SELECT 1 FROM json_each(v.industries) WHERE value=?)")
			args = append(args, ind)
		}
		if len(parts) > 0 {
			where += ` AND (` + strings.Join(parts, " OR ") + `)`
		}
	}

	// Apply user filters (collected, hideExpired, remote, leadership, sources,
	// unread) for list requests only; single-vacancy fetches ignore them.
	if vacancyID == 0 {
		fParts, fArgs := filterClauses(now, f, false)
		for _, p := range fParts {
			where += ` AND ` + p
		}
		args = append(args, fArgs...)
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
			&row.industries,
			&unread, &row.reason, &row.decidedAt,
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

// loadVacancyView returns the stage for the given (profile, vacancy) pair and
// the view-only vacancy fields. It answers NotFound when no vacancy row exists.
func loadVacancyView(ctx context.Context, database *db.DB, profileID, vacancyID int64) (stage string, vd viewData, err error) {
	err = database.QueryRowContext(ctx, `
		SELECT COALESCE(d.stage,'inbox'), v.url, v.apply_url, v.text_state, v.body
		FROM vacancy v LEFT JOIN decision d ON d.vacancy=v.id AND d.profile=?
		WHERE v.id=?`,
		profileID, vacancyID).Scan(&stage, &vd.url, &vd.applyURL, &vd.textState, &vd.body)
	if err == sql.ErrNoRows {
		return "", viewData{}, apperr.NotFound{Message: "Role not found."}
	}
	if err != nil {
		return "", viewData{}, fmt.Errorf("inbox: view data: %w", err)
	}
	rows, err := database.QueryContext(ctx,
		`SELECT source FROM vacancy_source WHERE vacancy_id=? ORDER BY rowid`, vacancyID)
	if err != nil {
		return "", viewData{}, fmt.Errorf("inbox: view sources: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var src string
		if err := rows.Scan(&src); err != nil {
			return "", viewData{}, fmt.Errorf("inbox: view sources scan: %w", err)
		}
		vd.sources = append(vd.sources, src)
	}
	return stage, vd, rows.Err()
}
