// Package inbox handles the vacancy list, decisions, seen and delete endpoints.
package inbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

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
	unread         bool
	stage          string
	reason         string
	decidedAt      string
	appliedAt      string
	appliedName    string
	expiresAt      string
}

const listCols = `v.id, v.company, v.title, v.location, v.published_at, v.published_basis, v.first_seen, v.tags,
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

// loadProfileSearch reads the search fields of a profile from the database.
func loadProfileSearch(ctx context.Context, database *db.DB, profileID int64) (candidate.Profile, error) {
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
	return candidate.Profile{
		TargetRoles: pd.TargetRoles,
		Keywords:    pd.Keywords,
		AllOf:       pd.AllOf,
	}, nil
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
// optionally filtered by query q. When vacancyID is non-zero only that vacancy is returned.
// vis is applied to inbox listings only (stage == "inbox" and vacancyID == 0);
// it is ignored for decided stages and for single-vacancy fetches.
func listRows(ctx context.Context, database *db.DB, profileID, vacancyID int64, stage, q string, vis candidate.Filter) ([]repoRow, error) {
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
