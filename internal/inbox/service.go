package inbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/sifty-works/sifty-server/internal/apperr"
	"github.com/sifty-works/sifty-server/internal/candidate"
	"github.com/sifty-works/sifty-server/internal/catalog"
	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/listing"
)

// Filters carries the user-supplied filter parameters for the vacancy list.
type Filters struct {
	Collected  int // 0 = any, 7 = last 7 days, 30 = last 30 days by first_seen
	Remote     bool
	Leadership bool
	Sources    []string // source keys; empty means all sources
	Unread     bool
}

// Stage is the inbox stage of a vacancy row.
type Stage string

const (
	StageInbox     Stage = "inbox"
	StageShortlist Stage = "shortlist"
	StageApplied   Stage = "applied"
	StageDismissed Stage = "dismissed"
)

// ConfirmDialog names the confirmation dialog to show before an action.
type ConfirmDialog string

const (
	ConfirmOutcome ConfirmDialog = "outcome"
	ConfirmDelete  ConfirmDialog = "delete"
)

// Row is one vacancy row in the API list response.
type Row struct {
	ID         int64    `json:"id"`
	Company    string   `json:"company"`
	Title      string   `json:"title"`
	Location   string   `json:"location"`
	Published  string   `json:"published"`
	Industries []string `json:"industries"`
	Unread     bool     `json:"unread"`
	Stage      Stage    `json:"stage"`
	Reason     string   `json:"reason,omitempty"`
	Applied    string   `json:"applied,omitempty"`
	Expired    bool     `json:"expired,omitempty"`
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
	nowStr := now.UTC().Format(time.RFC3339)
	var industries []string
	if rr.industries != "" && rr.industries != "[]" {
		json.Unmarshal([]byte(rr.industries), &industries) //nolint:errcheck
	}
	if industries == nil {
		industries = []string{}
	}
	return Row{
		ID:         rr.id,
		Company:    company,
		Title:      rr.title,
		Location:   locationText(rr.location),
		Published:  publicationLine(rr.publishedAt, rr.publishedBasis, now),
		Industries: industries,
		Unread:     rr.unread,
		Stage:      Stage(rr.stage),
		Reason:     rr.reason,
		Applied:    appliedText(rr.appliedAt, rr.appliedName, rr.stage, now),
		Expired:    rr.expiresAt != "" && rr.expiresAt <= nowStr,
	}
}

// profileVisibility loads the visibility filter for a profile.
// vocabNames restricts target industries to known names when non-nil.
// Returns Filter{SQL:"1=0"} when the profile cannot be loaded.
func profileVisibility(ctx context.Context, database *db.DB, profileID int64, vocabNames []string) candidate.Filter {
	p, err := candidate.ProfileSearch(ctx, database, profileID, vocabNames)
	if err != nil {
		return candidate.Filter{SQL: "1=0"}
	}
	return candidate.Visibility(p)
}

// buildCounts converts a stage-count map to a Counts value.
func buildCounts(cm map[string]int) Counts {
	return Counts{
		Inbox:     cm["inbox"],
		Shortlist: cm["shortlist"],
		Applied:   cm["applied"],
		Dismissed: cm["dismissed"],
		Archive:   cm["applied"] + cm["dismissed"],
	}
}

// loadCounts returns the per-stage vacancy counts for a profile,
// applying the profile's search visibility to the inbox count.
func loadCounts(ctx context.Context, database *db.DB, now time.Time, profileID int64, vocabNames []string) (Counts, error) {
	vis := profileVisibility(ctx, database, profileID, vocabNames)
	cm, err := stageCounts(ctx, database, now, profileID, vis)
	if err != nil {
		return Counts{}, err
	}
	return buildCounts(cm), nil
}

// rowAndCounts fetches one vacancy row and the profile counts as a shared tail for decision paths.
func rowAndCounts(ctx context.Context, database *db.DB, now func() time.Time, profileID, vacancyID int64, vocabNames []string) (Row, Counts, error) {
	vis := profileVisibility(ctx, database, profileID, vocabNames)
	_, row, _, err := loadVacancyFull(ctx, database, profileID, vacancyID)
	if err != nil {
		return Row{}, Counts{}, err
	}
	t := now()
	cm, err := stageCounts(ctx, database, t, profileID, vis)
	if err != nil {
		return Row{}, Counts{}, err
	}
	return toRow(row, t), buildCounts(cm), nil
}

// list returns the rows, counts, industry counts, source counts and total for
// the given profile, stage, query, industries filter and user Filters.
// vocabNames restricts the profile's target industries in the visibility filter.
// total is the stage count before the query and f are applied.
func list(ctx context.Context, database *db.DB, now func() time.Time, profileID int64, stage, q string, vocabNames []string, industries []string, f Filters) ([]Row, Counts, []IndustryCount, []SourceCount, int, error) {
	vis := profileVisibility(ctx, database, profileID, vocabNames)
	t := now()
	cm, err := stageCounts(ctx, database, t, profileID, vis)
	if err != nil {
		return nil, Counts{}, nil, nil, 0, err
	}
	indCounts, err := industryCounts(ctx, database, t, profileID, stage, q, vis, f)
	if err != nil {
		return nil, Counts{}, nil, nil, 0, err
	}
	srcCounts, err := sourceCounts(ctx, database, t, profileID, stage, q, vis, industries, f)
	if err != nil {
		return nil, Counts{}, nil, nil, 0, err
	}
	raw, err := listRows(ctx, database, t, profileID, stage, q, vis, f, industries...)
	if err != nil {
		return nil, Counts{}, nil, nil, 0, err
	}
	rows := make([]Row, len(raw))
	for i, rr := range raw {
		rows[i] = toRow(rr, t)
	}
	total := cm[stage]
	return rows, buildCounts(cm), indCounts, srcCounts, total, nil
}

// applyDecision validates and executes a stage transition.
// Returns the updated row and profile counts on success.
// vocabNames is passed to profileVisibility for the counts refresh.
func applyDecision(ctx context.Context, database *db.DB, now func() time.Time,
	vacancyID, profileID int64, from, to, reason string, confirmed bool, vocabNames []string) (Row, Counts, error) {

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
		return rowAndCounts(ctx, database, now, profileID, vacancyID, vocabNames)
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
	return rowAndCounts(ctx, database, now, profileID, vacancyID, vocabNames)
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
// hasFilters is true when any query or filter is active.
func emptyText(rowCount int, stage string, hasFilters bool) string {
	if rowCount > 0 {
		return ""
	}
	if hasFilters {
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

// --- Vacancy view types and functions ---

// Outcome is one answer shown in the outcome confirmation dialog.
type Outcome struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	To        Stage  `json:"to,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Confirmed bool   `json:"confirmed,omitempty"`
}

// Action is one available action on the vacancy page.
// To is the target stage for decision endpoints (from is the row's stage).
// Confirm names the dialog to show before the action ("outcome" or "delete").
// Outcomes lists the choices shown in the outcome dialog.
type Action struct {
	Key      string        `json:"key"`
	Label    string        `json:"label"`
	To       Stage         `json:"to,omitempty"`
	URL      string        `json:"url,omitempty"`
	Message  string        `json:"message,omitempty"`
	Confirm  ConfirmDialog `json:"confirm,omitempty"`
	Outcomes []Outcome     `json:"outcomes,omitempty"`
}

// ListingView describes the vacancy's text as shown on the vacancy page.
type ListingView struct {
	State string `json:"state"`
	HTML  string `json:"html"`
	Empty string `json:"empty,omitempty"`
}

// VacancySource is one source that carried a vacancy, as returned by the API.
// URL is the source address; it is omitted when the internal source identifier
// is not a valid HTTP address.
type VacancySource struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

// VacancyView is the vacancy page API response.
type VacancyView struct {
	Row
	Sources  []VacancySource `json:"sources"`
	Original string          `json:"original"`
	Status   string          `json:"status,omitempty"`
	Actions  []Action        `json:"actions"`
	Listing  ListingView     `json:"listing"`
}

// listingState returns the display text for a vacancy's text_state value.
func listingState(textState string) string {
	switch listing.TextState(textState) {
	case listing.TextFull:
		return "Full text"
	case listing.TextSummary:
		return "Listing summary only"
	case listing.TextBlocked:
		return "Blocked by site"
	case listing.TextRemoved:
		return "Text removed after 60 days unseen; it returns if the source shows the role again"
	}
	return "Not fetched"
}

// toVacancySource converts an internal source key to a VacancySource.
// When the key appears in names, that name is used. Otherwise the hostname of
// the key URL is used, with a leading "www." stripped. URL is set when src is
// a valid http(s) address.
func toVacancySource(src string, names map[string]string) VacancySource {
	name, srcURL := names[src], ""
	if u, err := url.Parse(src); err == nil && u.Host != "" {
		if name == "" {
			name = strings.TrimPrefix(strings.ToLower(u.Host), "www.")
		}
		if u.Scheme == "http" || u.Scheme == "https" {
			srcURL = src
		}
	}
	if name == "" {
		name = src
	}
	return VacancySource{Name: name, URL: srcURL}
}

// statusText returns the status line shown on the vacancy page.
func statusText(stage, reason, appliedAt string, t time.Time) string {
	if stage == "dismissed" {
		if reason != "" {
			return "Dismissed — " + reason
		}
		return "Dismissed"
	}
	if stage != "applied" || appliedAt == "" {
		return ""
	}
	at, err := time.Parse(time.RFC3339, appliedAt)
	if err != nil {
		return ""
	}
	return "Applied " + at.In(t.Location()).Format("2 Jan 2006")
}

// validApplyURL reports whether raw is an http(s) URL with no credentials.
func validApplyURL(raw string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.User == nil
}

// outcomeButtons are the choices in the apply-outcome dialog, used by both the
// apply and confirm actions in the shortlist stage.
var outcomeButtons = []Outcome{
	{Key: "not-yet", Label: "Not yet"},
	{Key: "stale", Label: "No, vacancy is stale", To: StageDismissed, Reason: "vacancy is stale"},
	{Key: "closed", Label: "No, vacancy is closed", To: StageDismissed, Reason: "vacancy is closed"},
	{Key: "unsuitable", Label: "No, not suitable", To: StageDismissed, Reason: "not suitable"},
	{Key: "applied", Label: "Yes, applied", To: StageApplied, Confirmed: true},
}

// stageActions returns the actions available in stage, in the order the page
// shows them. Labels carry no keyboard hints; to is the decision target stage;
// confirm names the dialog the client must show before proceeding.
func stageActions(stage, vacURL, applyURL, company string) []Action {
	switch stage {
	case "inbox":
		out := make([]Action, 0, 3)
		if company != "" {
			out = append(out, Action{Key: "block", Label: "Block company"})
		}
		return append(out,
			Action{Key: "dismiss", Label: "Dismiss", To: StageDismissed},
			Action{Key: "select", Label: "Select", To: StageShortlist})
	case "shortlist":
		apply := buildApplyAction(vacURL, applyURL)
		return []Action{
			{Key: "inbox", Label: "Move to Inbox", To: StageInbox},
			apply,
			{Key: "confirm", Label: "Confirm application…", To: StageApplied, Confirm: ConfirmOutcome,
				Outcomes: outcomeButtons},
			{Key: "delete", Label: "Delete role…", Confirm: ConfirmDelete},
		}
	case "applied":
		return []Action{
			{Key: "undo", Label: "Undo", To: StageShortlist},
			{Key: "delete", Label: "Delete role…", Confirm: ConfirmDelete},
		}
	case "dismissed":
		return []Action{
			{Key: "restore", Label: "Restore to Inbox", To: StageInbox},
			{Key: "delete", Label: "Delete role…", Confirm: ConfirmDelete},
		}
	}
	return []Action{}
}

// buildApplyAction builds the apply Action for the shortlist stage.
// It carries a URL when a valid http(s) address without credentials is found,
// otherwise a message. Labels carry no keyboard hints.
// Confirm and Outcomes are set so the client shows the outcome dialog after opening the URL.
func buildApplyAction(vacURL, applyURL string) Action {
	if validApplyURL(applyURL) {
		return Action{Key: "apply", Label: "Apply", URL: applyURL, Confirm: ConfirmOutcome, Outcomes: outcomeButtons}
	}
	if validApplyURL(vacURL) {
		return Action{Key: "apply", Label: "Apply", URL: vacURL, Confirm: ConfirmOutcome, Outcomes: outcomeButtons}
	}
	return Action{Key: "apply", Label: "Apply",
		Message:  "No valid employer or original listing URL is available. The role stays in Shortlist.",
		Confirm:  ConfirmOutcome,
		Outcomes: outcomeButtons,
	}
}

// vacancyView loads and assembles the vacancy page response for a resolved
// vacancy and profile. sourceNames maps source keys to their configured display names.
func vacancyView(ctx context.Context, database *db.DB, now func() time.Time,
	vacancyID, profileID int64, sourceNames map[string]string) (VacancyView, error) {

	t := now()
	stage, row, vd, err := loadVacancyFull(ctx, database, profileID, vacancyID)
	if err != nil {
		return VacancyView{}, err
	}

	sources := make([]VacancySource, 0, len(vd.sources))
	for _, src := range vd.sources {
		sources = append(sources, toVacancySource(src, sourceNames))
	}

	html, err := catalog.Body(vd.body)
	if err != nil {
		return VacancyView{}, fmt.Errorf("inbox: view: body: %w", err)
	}
	lv := ListingView{State: listingState(vd.textState), HTML: html}
	if html == "" {
		lv.Empty = "A full description has not been retrieved. Open the original listing; missing text is not an empty job."
	}

	apiRow := toRow(row, t)
	return VacancyView{
		Row:      apiRow,
		Sources:  sources,
		Original: vd.url,
		Status:   statusText(stage, row.reason, row.appliedAt, t),
		Actions:  stageActions(stage, vd.url, vd.applyURL, row.company),
		Listing:  lv,
	}, nil
}
