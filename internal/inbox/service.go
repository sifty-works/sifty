package inbox

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/sifty-works/sifty/internal/apperr"
	"github.com/sifty-works/sifty/internal/db"
)

// Row is one vacancy row in the API list response.
type Row struct {
	ID        int64  `json:"id"`
	Company   string `json:"company"`
	Title     string `json:"title"`
	Location  string `json:"location"`
	Published string `json:"published"`
	Unread    bool   `json:"unread"`
	Stage     string `json:"stage"`
	Reason    string `json:"reason,omitempty"`
	Applied   string `json:"applied,omitempty"`
}

// Counts holds per-stage vacancy counts for one profile.
type Counts struct {
	Inbox     int `json:"inbox"`
	Shortlist int `json:"shortlist"`
	Applied   int `json:"applied"`
	Dismissed int `json:"dismissed"`
	Archive   int `json:"archive"`
}

// toRow converts a repoRow to the API Row type.
func toRow(rr repoRow, now time.Time) Row {
	company := rr.company
	if company == "" {
		company = "Unknown"
	}
	return Row{
		ID:        rr.id,
		Company:   company,
		Title:     rr.title,
		Location:  locationText(rr.location),
		Published: publicationLine(rr.publishedAt, rr.publishedBasis, now),
		Unread:    rr.unread,
		Stage:     rr.stage,
		Reason:    rr.reason,
		Applied:   appliedText(rr.appliedAt, rr.appliedName, rr.stage, now),
	}
}

// loadCounts returns the per-stage vacancy counts for a profile.
func loadCounts(ctx context.Context, database *db.DB, profileID int64) (Counts, error) {
	cm, err := stageCounts(ctx, database, profileID)
	if err != nil {
		return Counts{}, err
	}
	return Counts{
		Inbox:     cm["inbox"],
		Shortlist: cm["shortlist"],
		Applied:   cm["applied"],
		Dismissed: cm["dismissed"],
		Archive:   cm["applied"] + cm["dismissed"],
	}, nil
}

// rowAndCounts fetches one vacancy row and the profile counts as a shared tail for decision paths.
func rowAndCounts(ctx context.Context, database *db.DB, now func() time.Time, profileID, vacancyID int64, stage string) (Row, Counts, error) {
	raws, err := listRows(ctx, database, profileID, vacancyID, stage, "")
	if err != nil {
		return Row{}, Counts{}, err
	}
	if len(raws) == 0 {
		return Row{}, Counts{}, apperr.NotFound{Message: "Role not found."}
	}
	counts, err := loadCounts(ctx, database, profileID)
	if err != nil {
		return Row{}, Counts{}, err
	}
	return toRow(raws[0], now()), counts, nil
}

// list returns the rows, counts and empty text for the given profile, stage and query.
func list(ctx context.Context, database *db.DB, now func() time.Time, profileID int64, stage, q string) ([]Row, Counts, error) {
	counts, err := loadCounts(ctx, database, profileID)
	if err != nil {
		return nil, Counts{}, err
	}
	raw, err := listRows(ctx, database, profileID, 0, stage, q)
	if err != nil {
		return nil, Counts{}, err
	}
	t := now()
	rows := make([]Row, len(raw))
	for i, rr := range raw {
		rows[i] = toRow(rr, t)
	}
	return rows, counts, nil
}

// applyDecision validates and executes a stage transition.
// Returns the updated row and profile counts on success.
func applyDecision(ctx context.Context, database *db.DB, now func() time.Time,
	vacancyID, profileID int64, from, to, reason string, confirmed bool) (Row, Counts, error) {

	// Load current stage.
	var currentStage string
	err := database.QueryRowContext(ctx,
		`SELECT COALESCE(d.stage,'inbox') FROM vacancy v LEFT JOIN decision d ON d.vacancy=v.id AND d.profile=? WHERE v.id=?`,
		profileID, vacancyID).Scan(&currentStage)
	if err == sql.ErrNoRows {
		return Row{}, Counts{}, apperr.NotFound{Message: "Role not found."}
	}
	if err != nil {
		return Row{}, Counts{}, fmt.Errorf("inbox: decision: load stage: %w", err)
	}

	// Stale from.
	if from != currentStage {
		return Row{}, Counts{}, apperr.Conflict{
			Message: fmt.Sprintf("job %d changed while this outcome was being saved; reload and try again", vacancyID),
		}
	}

	// No-op: same stage.
	if from == to {
		return rowAndCounts(ctx, database, now, profileID, vacancyID, from)
	}

	// Validate move.
	if err := validateMove(from, to, reason, confirmed); err != nil {
		return Row{}, Counts{}, err
	}

	nowStr := now().UTC().Format(time.RFC3339)
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return Row{}, Counts{}, fmt.Errorf("inbox: decision: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	switch to {
	case "inbox":
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM decision WHERE profile=? AND vacancy=?`, profileID, vacancyID); err != nil {
			return Row{}, Counts{}, fmt.Errorf("inbox: decision: delete: %w", err)
		}
	default:
		if _, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO decision (profile, vacancy, stage, reason, decided_at) VALUES (?,?,?,?,?)`,
			profileID, vacancyID, to, reason, nowStr); err != nil {
			return Row{}, Counts{}, fmt.Errorf("inbox: decision: upsert: %w", err)
		}
		if to == "applied" {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO application (vacancy, applied_at, profile, profile_name) SELECT ?,?,id,json_extract(data,'$.name') FROM profile WHERE id=?`,
				vacancyID, nowStr, profileID); err != nil {
				return Row{}, Counts{}, fmt.Errorf("inbox: decision: application: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return Row{}, Counts{}, fmt.Errorf("inbox: decision: commit: %w", err)
	}
	return rowAndCounts(ctx, database, now, profileID, vacancyID, to)
}

// validateMove returns an error when the stage transition is not allowed.
func validateMove(from, to, reason string, confirmed bool) error {
	inv := apperr.Invalid{Message: "the role is no longer awaiting an application decision; reload and try again"}
	// Reason only allowed on shortlist → dismissed.
	if reason != "" && !(from == "shortlist" && to == "dismissed") {
		return inv
	}
	if len([]rune(reason)) > 200 {
		return apperr.Invalid{Message: "Reason must be at most 200 characters."}
	}
	switch from + "→" + to {
	case "inbox→shortlist", "inbox→dismissed",
		"shortlist→inbox", "shortlist→dismissed",
		"applied→shortlist", "dismissed→inbox":
		return nil
	case "shortlist→applied":
		if !confirmed {
			return inv
		}
		return nil
	}
	return inv
}

// locationText returns the location string or "Location unstated".
func locationText(loc string) string {
	if loc == "" {
		return "Location unstated"
	}
	return loc
}

// publicationLine formats the publication line for a vacancy.
func publicationLine(publishedAt, publishedBasis string, now time.Time) string {
	if publishedAt == "" {
		return "Publication date not reported"
	}
	t, err := time.Parse(time.RFC3339, publishedAt)
	if err != nil {
		return "Publication date not reported"
	}
	loc := now.Location()
	dateStr := t.In(loc).Format("2 Jan 2006")
	if publishedBasis != "" {
		return "Published " + dateStr + " · " + ageText(t, now)
	}
	return "Source date " + dateStr + " (unverified)"
}

// ageText returns a human-readable age string.
func ageText(published, now time.Time) string {
	loc := now.Location()
	py, pm, pd := published.In(loc).Date()
	ny, nm, nd := now.In(loc).Date()
	pubDate := time.Date(py, pm, pd, 0, 0, 0, 0, loc)
	nowDate := time.Date(ny, nm, nd, 0, 0, 0, 0, loc)
	days := int(nowDate.Sub(pubDate).Hours() / 24)
	if days < 0 {
		days = 0
	}
	switch {
	case days == 0:
		return "today"
	case days == 1:
		return "1 day"
	case days < 14:
		return fmt.Sprintf("%d days", days)
	case days < 63:
		return fmt.Sprintf("%d weeks", days/7)
	default:
		return fmt.Sprintf("%d months", days/30)
	}
}

// appliedText formats the applied line for a vacancy row.
func appliedText(appliedAt, profileName, stage string, now time.Time) string {
	if appliedAt == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, appliedAt)
	if err != nil {
		return ""
	}
	date := t.In(now.Location()).Format("2 Jan 2006")
	if stage == "applied" {
		return "Applied " + date
	}
	return "Applied " + date + " from " + profileName
}

// emptyText returns the empty-state text for the stage.
func emptyText(rowCount int, stage, q string) string {
	if rowCount > 0 {
		return ""
	}
	if q != "" {
		return "No matching roles. Adjust your filters."
	}
	switch stage {
	case "shortlist":
		return "Nothing shortlisted yet — press Y in Inbox."
	case "applied":
		return "No applications yet."
	case "dismissed":
		return "No dismissed roles."
	}
	return "Inbox zero. Collect more jobs in Settings → Sources."
}
