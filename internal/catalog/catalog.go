// Package catalog owns the vacancy tables and the one path from a received
// posting to a stored vacancy.
package catalog

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/sifty-works/sifty/internal/db"
	"github.com/sifty-works/sifty/internal/listing"
)

// Outcome describes the result of an Upsert call.
type Outcome int

const (
	OutcomeNew        Outcome = iota + 1
	OutcomeUpdated            // text changed
	OutcomeUnchanged          // matched, text same
	OutcomeMerged             // multiple matches collapsed into one
	OutcomeTombstoned         // address is on the removed list
)

// String returns a human-readable name for the outcome.
func (o Outcome) String() string {
	switch o {
	case OutcomeNew:
		return "New"
	case OutcomeUpdated:
		return "Updated"
	case OutcomeUnchanged:
		return "Unchanged"
	case OutcomeMerged:
		return "Merged"
	case OutcomeTombstoned:
		return "Tombstoned"
	}
	return fmt.Sprintf("Outcome(%d)", int(o))
}

// Catalog owns the vacancy tables.
type Catalog struct {
	db  *db.DB
	now func() time.Time
}

// New returns a Catalog backed by the given database.
// now is called each time the current time is needed; pass time.Now in production.
func New(database *db.DB, now func() time.Time) *Catalog {
	return &Catalog{db: database, now: now}
}

// Body decompresses a stored vacancy body (compress/flate).
func Body(blob []byte) (string, error) {
	if len(blob) == 0 {
		return "", nil
	}
	r := flate.NewReader(bytes.NewReader(blob))
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("catalog: body: decompress: %w", err)
	}
	return string(b), nil
}

// Upsert stores a posting from a source, following the matching and update
// rules. It calls listing.Normalize on the posting's text itself.
// src is the source's opaque address key.
func (c *Catalog) Upsert(ctx context.Context, p listing.Posting, src string) (int64, Outcome, error) {
	canonURL, err := listing.Canonical(p.URL)
	if err != nil {
		return 0, 0, fmt.Errorf("catalog: upsert: canonical url: %w", err)
	}

	// The apply address is kept only when it is one concrete vacancy in an
	// applicant tracking system, never the root of a board or another page.
	var canonApply, atsApply string
	if p.ApplyURL != "" {
		if ca, ok := listing.ConcreteATS(p.ApplyURL); ok {
			canonApply = ca
			atsApply = ca
		}
	}

	company := p.Company
	if company == "" {
		company = "Unknown"
	}
	identity := listing.Identity(company, p.Title)

	now := c.now().UTC()
	nowStr := now.Format(time.RFC3339)

	bodyHTML := listing.Normalize(p.Text, p.TextKind)
	bodyPlain := listing.PlainText(bodyHTML)
	newHash := contentHash(bodyPlain)

	newBody, err := compressBody(bodyHTML)
	if err != nil {
		return 0, 0, fmt.Errorf("catalog: upsert: compress: %w", err)
	}

	tagsJSON := marshalTags(p.Tags)
	fieldsJSON := marshalFields(p.Fields)
	// A future publication date is never stored.
	publishedAt := ""
	publishedBasis := ""
	if !p.Published.IsZero() && !p.Published.After(now) {
		publishedAt = formatTime(p.Published)
		publishedBasis = p.PublishedBasis
	}
	expiresAt := formatTime(p.Expires)

	textState := string(p.TextState)
	if textState == "" {
		textState = string(listing.TextNotFetched)
	}

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("catalog: upsert: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Tombstone check: url-based and legacy-key-based.
	tombKeys := []string{"url:" + canonURL}
	if lk := listing.LegacyKey(p.URL); lk != "" {
		tombKeys = append(tombKeys, "legacy:"+lk)
	}
	if p.ApplyURL != "" {
		if lk := listing.LegacyKey(p.ApplyURL); lk != "" {
			tombKeys = append(tombKeys, "legacy:"+lk)
		}
	}
	tombstoned, err := anyTombstoned(ctx, tx, tombKeys)
	if err != nil {
		return 0, 0, fmt.Errorf("catalog: upsert: tombstone check: %w", err)
	}
	if tombstoned {
		return 0, OutcomeTombstoned, nil
	}

	// Find matching vacancies.
	matchIDs, err := findMatches(ctx, tx, canonURL, atsApply, canonApply, identity, company, now)
	if err != nil {
		return 0, 0, fmt.Errorf("catalog: upsert: match: %w", err)
	}

	// New vacancy.
	if len(matchIDs) == 0 {
		id, err := insertVacancy(ctx, tx, vacancyData{
			identity:       identity,
			title:          p.Title,
			company:        company,
			url:            canonURL,
			applyURL:       canonApply,
			location:       p.Location,
			tagsJSON:       tagsJSON,
			fieldsJSON:     fieldsJSON,
			publishedAt:    publishedAt,
			publishedBasis: publishedBasis,
			expiresAt:      expiresAt,
			firstSeen:      nowStr,
			lastSeen:       nowStr,
			textState:      textState,
			contentHash:    newHash,
			version:        1,
			body:           newBody,
		})
		if err != nil {
			return 0, 0, fmt.Errorf("catalog: upsert: insert: %w", err)
		}
		if err := insertSource(ctx, tx, id, src, canonURL, nowStr); err != nil {
			return 0, 0, fmt.Errorf("catalog: upsert: insert source: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return 0, 0, fmt.Errorf("catalog: upsert: commit: %w", err)
		}
		return id, OutcomeNew, nil
	}

	// Multiple matches: merge all into the one with the lowest id.
	if len(matchIDs) > 1 {
		sort.Slice(matchIDs, func(i, j int) bool { return matchIDs[i] < matchIDs[j] })
		if err := performMerge(ctx, tx, matchIDs); err != nil {
			return 0, 0, fmt.Errorf("catalog: upsert: merge: %w", err)
		}
	}
	vacancyID := matchIDs[0]

	// Load existing vacancy.
	var ex existingRow
	if err := loadVacancy(ctx, tx, vacancyID, &ex); err != nil {
		return 0, 0, fmt.Errorf("catalog: upsert: load: %w", err)
	}

	// Apply update rules.
	upd := ex // start from existing

	upd.title = p.Title // always from posting
	if company != "Unknown" && company != "" {
		upd.company = company
		upd.identity = listing.Identity(company, p.Title)
	} else {
		upd.identity = listing.Identity(ex.company, p.Title)
	}
	if p.Location != "" {
		upd.location = p.Location
	}
	if len(p.Tags) > 0 {
		upd.tagsJSON = tagsJSON
	}
	upd.fieldsJSON = mergeFields(ex.fieldsJSON, fieldsJSON)
	if ex.applyURL == "" && canonApply != "" {
		upd.applyURL = canonApply
	}
	// Publication date: set only when none stored (and publishedAt is already
	// filtered to exclude future dates by the time we reach here).
	if ex.publishedAt == "" && publishedAt != "" {
		upd.publishedAt = publishedAt
		upd.publishedBasis = publishedBasis
	}
	// Expiry: updated only when the posting states one.
	if expiresAt != "" {
		upd.expiresAt = expiresAt
	}

	// Text replacement.
	textChanged := false
	if shouldReplaceText(listing.TextState(ex.textState), p.Text, p.TextState, ex.contentHash, newHash) {
		upd.contentHash = newHash
		upd.body = newBody
		upd.textState = textState
		upd.version = ex.version + 1
		textChanged = true
	}
	upd.lastSeen = nowStr

	if _, err := tx.ExecContext(ctx, `
		UPDATE vacancy SET
			identity=?, title=?, company=?, location=?, tags=?, source_fields=?,
			apply_url=?, published_at=?, published_basis=?, expires_at=?,
			last_seen=?, text_state=?, content_hash=?, version=?, body=?
		WHERE id=?`,
		upd.identity, upd.title, upd.company, upd.location, upd.tagsJSON, upd.fieldsJSON,
		upd.applyURL, upd.publishedAt, upd.publishedBasis, upd.expiresAt,
		upd.lastSeen, upd.textState, upd.contentHash, upd.version, upd.body,
		vacancyID); err != nil {
		return 0, 0, fmt.Errorf("catalog: upsert: update: %w", err)
	}

	if err := upsertSource(ctx, tx, vacancyID, src, canonURL, nowStr); err != nil {
		return 0, 0, fmt.Errorf("catalog: upsert: source: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("catalog: upsert: commit: %w", err)
	}

	if len(matchIDs) > 1 {
		return vacancyID, OutcomeMerged, nil
	}
	if textChanged {
		return vacancyID, OutcomeUpdated, nil
	}
	return vacancyID, OutcomeUnchanged, nil
}

// --- internal helpers ---

type vacancyData struct {
	identity, title, company, url, applyURL, location string
	tagsJSON, fieldsJSON                              string
	publishedAt, publishedBasis, expiresAt            string
	firstSeen, lastSeen                               string
	textState, contentHash                            string
	version                                           int
	body                                              []byte
}

type existingRow struct {
	id             int64
	identity       string
	title          string
	company        string
	url            string
	applyURL       string
	location       string
	tagsJSON       string
	fieldsJSON     string
	publishedAt    string
	publishedBasis string
	expiresAt      string
	firstSeen      string
	lastSeen       string
	textState      string
	contentHash    string
	version        int
	body           []byte
}

func insertVacancy(ctx context.Context, tx *sql.Tx, v vacancyData) (int64, error) {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO vacancy
			(identity, title, company, url, apply_url, location, tags, source_fields,
			 published_at, published_basis, expires_at,
			 first_seen, last_seen, text_state, content_hash, version, body)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		v.identity, v.title, v.company, v.url, v.applyURL, v.location,
		v.tagsJSON, v.fieldsJSON,
		v.publishedAt, v.publishedBasis, v.expiresAt,
		v.firstSeen, v.lastSeen, v.textState, v.contentHash, v.version, v.body)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func loadVacancy(ctx context.Context, tx *sql.Tx, id int64, ex *existingRow) error {
	return tx.QueryRowContext(ctx, `
		SELECT id, identity, title, company, url, apply_url, location, tags, source_fields,
		       published_at, published_basis, expires_at,
		       first_seen, last_seen, text_state, content_hash, version, body
		FROM vacancy WHERE id = ?`, id).Scan(
		&ex.id, &ex.identity, &ex.title, &ex.company, &ex.url, &ex.applyURL, &ex.location,
		&ex.tagsJSON, &ex.fieldsJSON,
		&ex.publishedAt, &ex.publishedBasis, &ex.expiresAt,
		&ex.firstSeen, &ex.lastSeen, &ex.textState, &ex.contentHash, &ex.version, &ex.body)
}

func insertSource(ctx context.Context, tx *sql.Tx, vacancyID int64, src, u, seenAt string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO vacancy_source (vacancy_id, source, url, seen_at) VALUES (?, ?, ?, ?)`,
		vacancyID, src, u, seenAt)
	return err
}

func upsertSource(ctx context.Context, tx *sql.Tx, vacancyID int64, src, u, seenAt string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO vacancy_source (vacancy_id, source, url, seen_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(url) DO UPDATE SET seen_at = excluded.seen_at`,
		vacancyID, src, u, seenAt)
	return err
}

// findMatches returns all vacancy ids that match the posting.
// atsApply is the ConcreteATS form of the apply URL (empty when not concrete).
// canonApply is the best canonical form used for the conflict guard in rule (c).
func findMatches(ctx context.Context, tx *sql.Tx, canonURL, atsApply, canonApply, identity, company string, now time.Time) ([]int64, error) {
	var ids []int64

	// Match (a): canonical URL already in vacancy_source.
	var aid int64
	err := tx.QueryRowContext(ctx, `SELECT vacancy_id FROM vacancy_source WHERE url = ?`, canonURL).Scan(&aid)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("match by url: %w", err)
	}
	if err == nil {
		ids = appendUnique(ids, aid)
	}

	// Match (b): same concrete ATS apply address (only when posting has one).
	if atsApply != "" {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM vacancy WHERE apply_url = ?`, atsApply)
		if err != nil {
			return nil, fmt.Errorf("match by apply: %w", err)
		}
		for rows.Next() {
			var bid int64
			if err := rows.Scan(&bid); err != nil {
				rows.Close()
				return nil, err
			}
			ids = appendUnique(ids, bid)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}

	// Match (c): same identity, company not Unknown, different host, no conflicting apply URL.
	if company != "Unknown" {
		newHost := urlHost(canonURL)
		rows, err := tx.QueryContext(ctx,
			`SELECT id, url, apply_url FROM vacancy WHERE identity = ? AND company != 'Unknown'`,
			identity)
		if err != nil {
			return nil, fmt.Errorf("match by identity: %w", err)
		}
		for rows.Next() {
			var cid int64
			var storedURL, storedApply string
			if err := rows.Scan(&cid, &storedURL, &storedApply); err != nil {
				rows.Close()
				return nil, err
			}
			if urlHost(storedURL) == newHost {
				continue // same host — no match
			}
			// Apply conflict: both set and different.
			if canonApply != "" && storedApply != "" && canonApply != storedApply {
				continue
			}
			ids = appendUnique(ids, cid)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}

	return ids, nil
}

// shouldReplaceText reports whether the stored text should be replaced.
func shouldReplaceText(storedState listing.TextState, newText string, newState listing.TextState, oldHash, newHash string) bool {
	if newText == "" {
		return false
	}
	if newState == listing.TextBlocked {
		return false
	}
	// A vacancy whose text was removed or not fetched always takes non-empty text.
	if storedState == listing.TextRemoved || storedState == listing.TextNotFetched {
		return true
	}
	// A summary never replaces full text.
	if storedState == listing.TextFull && newState == listing.TextSummary {
		return false
	}
	// Only replace when the plain text actually differs.
	return oldHash != newHash
}

// compressBody compresses HTML with compress/flate at best compression.
// Returns an empty (non-nil) slice when html is empty so NOT NULL is satisfied.
func compressBody(html string) ([]byte, error) {
	if html == "" {
		return []byte{}, nil
	}
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write([]byte(html)); err != nil {
		w.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// contentHash returns the hex SHA-256 of plain text.
func contentHash(plainText string) string {
	if plainText == "" {
		return ""
	}
	h := sha256.Sum256([]byte(plainText))
	return hex.EncodeToString(h[:])
}

// marshalTags serialises a string slice as a JSON array, returning "[]" for nil.
func marshalTags(tags []string) string {
	if len(tags) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(tags)
	return string(b)
}

// marshalFields serialises a map as a JSON object, returning "{}" for nil.
func marshalFields(fields map[string]string) string {
	if len(fields) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(fields)
	return string(b)
}

// mergeFields merges two JSON objects, with incoming values winning.
func mergeFields(existing, incoming string) string {
	var base, over map[string]string
	json.Unmarshal([]byte(existing), &base) //nolint:errcheck
	json.Unmarshal([]byte(incoming), &over) //nolint:errcheck
	if base == nil {
		base = make(map[string]string)
	}
	for k, v := range over {
		base[k] = v
	}
	b, _ := json.Marshal(base)
	return string(b)
}

// formatTime returns the UTC RFC 3339 representation of t, or "" if zero.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// urlHost returns the lowercase host of a URL, or "" on error.
func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

// appendUnique appends id to ids only if it is not already present.
func appendUnique(ids []int64, id int64) []int64 {
	for _, existing := range ids {
		if existing == id {
			return ids
		}
	}
	return append(ids, id)
}

// anyTombstoned reports whether any of the given keys appears in the
// tombstone table.
func anyTombstoned(ctx context.Context, tx *sql.Tx, keys []string) (bool, error) {
	for _, key := range keys {
		if key == "" {
			continue
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tombstone WHERE key = ?`, key).Scan(&n); err != nil {
			return false, err
		}
		if n > 0 {
			return true, nil
		}
	}
	return false, nil
}

// performMerge merges all vacancies in matchIDs (sorted low to high) into the
// first (lowest-id) one. Before deletion the kept vacancy receives the best
// data from all matched rows: earliest first_seen; earliest publication date;
// apply URL when it has none; best text (full > summary > none, lowest id wins
// tie; version increments when text is replaced).
// matchIDs must already be sorted ascending.
func performMerge(ctx context.Context, tx *sql.Tx, matchIDs []int64) error {
	// Load all matched vacancies.
	rows := make([]existingRow, len(matchIDs))
	for i, id := range matchIDs {
		if err := loadVacancy(ctx, tx, id, &rows[i]); err != nil {
			return fmt.Errorf("load vacancy %d: %w", id, err)
		}
	}

	upd := rows[0] // kept = lowest id

	for _, r := range rows[1:] {
		// Earliest first_seen.
		upd.firstSeen = pickEarlier(upd.firstSeen, r.firstSeen)
		// Earliest non-empty publication date.
		if r.publishedAt != "" && (upd.publishedAt == "" || r.publishedAt < upd.publishedAt) {
			upd.publishedAt = r.publishedAt
			upd.publishedBasis = r.publishedBasis
		}
		// Apply URL when kept has none.
		if upd.applyURL == "" && r.applyURL != "" {
			upd.applyURL = r.applyURL
		}
		// Best text: full > summary > none; lowest id wins ties (rows are asc).
		if betterText(listing.TextState(r.textState), listing.TextState(upd.textState)) {
			upd.textState = r.textState
			upd.contentHash = r.contentHash
			upd.body = r.body
			upd.version++
		}
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE vacancy SET
			first_seen=?, published_at=?, published_basis=?,
			apply_url=?, text_state=?, content_hash=?, version=?, body=?
		WHERE id=?`,
		upd.firstSeen, upd.publishedAt, upd.publishedBasis,
		upd.applyURL, upd.textState, upd.contentHash, upd.version, upd.body,
		upd.id); err != nil {
		return fmt.Errorf("update kept %d: %w", upd.id, err)
	}

	for _, r := range rows[1:] {
		oldID := r.id
		if _, err := tx.ExecContext(ctx,
			`UPDATE vacancy_source SET vacancy_id = ? WHERE vacancy_id = ?`,
			upd.id, oldID); err != nil {
			return fmt.Errorf("move sources from %d: %w", oldID, err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE vacancy_alias SET new_id = ? WHERE new_id = ?`,
			upd.id, oldID); err != nil {
			return fmt.Errorf("repoint aliases from %d: %w", oldID, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO vacancy_alias (old_id, new_id) VALUES (?, ?)`,
			oldID, upd.id); err != nil {
			return fmt.Errorf("insert alias for %d: %w", oldID, err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM vacancy WHERE id = ?`, oldID); err != nil {
			return fmt.Errorf("delete merged %d: %w", oldID, err)
		}
	}
	return nil
}

// textRank returns the numeric rank of a text state: full=2, summary=1, else 0.
func textRank(s listing.TextState) int {
	switch s {
	case listing.TextFull:
		return 2
	case listing.TextSummary:
		return 1
	}
	return 0
}

// betterText reports whether candidate is strictly better than current.
func betterText(candidate, current listing.TextState) bool {
	return textRank(candidate) > textRank(current)
}

// pickEarlier returns the earlier of two non-empty UTC RFC 3339 timestamps.
// An empty a is replaced by any non-empty b.
func pickEarlier(a, b string) string {
	if b == "" {
		return a
	}
	if a == "" || b < a {
		return b
	}
	return a
}
