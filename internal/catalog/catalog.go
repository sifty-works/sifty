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

	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/listing"
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

// Catalog owns the vacancy tables.
type Catalog struct {
	db     *db.DB
	now    func() time.Time
	assign func(title, tags, text string) []string // nil means no assignment
}

// New returns a Catalog backed by the given database.
// now is called each time the current time is needed; pass time.Now in production.
// assign is an optional function called to derive industry names from a vacancy's
// title, tags and plain text. When omitted, industries are not assigned.
func New(database *db.DB, now func() time.Time, assign ...func(title, tags, text string) []string) *Catalog {
	c := &Catalog{db: database, now: now}
	if len(assign) > 0 {
		c.assign = assign[0]
	}
	return c
}

// Encode compresses html and returns the body blob and the content hash.
// The hash is the SHA-256 hex of the plain-text form of html.
// It is the single implementation used by both Upsert and the import path,
// so stored bodies and hashes are always consistent.
func Encode(html string) (body []byte, hash string, err error) {
	plain := listing.PlainText(html)
	hash = contentHash(plain)
	body, err = compressBody(html)
	if err != nil {
		return nil, "", fmt.Errorf("catalog: encode: %w", err)
	}
	return body, hash, nil
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

// WriteFTS writes the FTS index row for a vacancy inside an open transaction.
// Any existing row for id is removed first. A row is always inserted with the
// normalised title, tags and body text so that visibility and search can match
// against title and tags even when no body has been fetched yet.
// It is used by every path that sets or replaces a vacancy title, tags or body.
func WriteFTS(ctx context.Context, tx *sql.Tx, id int64, title, tagsJSON string, body []byte) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM vacancy_fts WHERE rowid = ?`, id); err != nil {
		return fmt.Errorf("catalog: fts delete %d: %w", id, err)
	}
	var plainText string
	if len(body) > 0 {
		h, err := Body(body)
		if err != nil {
			return fmt.Errorf("catalog: fts body %d: %w", id, err)
		}
		plainText = listing.PlainText(h)
	}
	var tags []string
	json.Unmarshal([]byte(tagsJSON), &tags) //nolint:errcheck
	tagsText := strings.Join(tags, " ")
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO vacancy_fts(rowid, title, tags, text) VALUES(?, ?, ?, ?)`,
		id, listing.FTSNorm(title), listing.FTSNorm(tagsText), listing.FTSNorm(plainText)); err != nil {
		return fmt.Errorf("catalog: fts insert %d: %w", id, err)
	}
	return nil
}

// resolved holds the computed canonical keys, company, identity and matches
// for a posting, returned by resolve.
type resolved struct {
	canonURL, canonApply, company, identity string
	removed                                 bool
	matchIDs                                []int64
}

// resolve computes the canonical address, the ATS apply address, company and
// identity for p, checks the tombstone table and finds matching vacancies.
// The apply address is kept only when it points to one concrete vacancy in an
// applicant tracking system; a posting whose own address is such a vacancy
// applies there.
func resolve(ctx context.Context, tx *sql.Tx, p listing.Posting) (resolved, error) {
	canon, err := listing.Canonical(p.URL)
	if err != nil {
		return resolved{}, fmt.Errorf("canonical url: %w", err)
	}
	var apply string
	for _, raw := range []string{p.ApplyURL, p.URL} {
		if raw != "" {
			if ca, ok := listing.ConcreteATS(raw); ok {
				apply = ca
				break
			}
		}
	}
	co, id := p.Company, listing.Identity(p.Company, p.Title)
	gone, err := anyTombstoned(ctx, tx, tombstoneKeys(canon, p.URL, p.ApplyURL))
	if err != nil {
		return resolved{}, fmt.Errorf("tombstone: %w", err)
	}
	if gone {
		return resolved{removed: true}, nil
	}
	ms, err := findMatches(ctx, tx, canon, apply, apply, id, co)
	if err != nil {
		return resolved{}, fmt.Errorf("match: %w", err)
	}
	return resolved{canonURL: canon, canonApply: apply, company: co, identity: id, matchIDs: ms}, nil
}

// Upsert stores a posting from a source, following the matching and update
// rules. It calls listing.Normalize on the posting's text itself.
// src is the source's opaque address key.
func (c *Catalog) Upsert(ctx context.Context, p listing.Posting, src string) (int64, Outcome, error) {
	now := c.now().UTC()
	nowStr := now.Format(time.RFC3339)

	bodyHTML := listing.Normalize(p.Text)
	newBody, newHash, err := Encode(bodyHTML)
	if err != nil {
		return 0, 0, fmt.Errorf("catalog: upsert: %w", err)
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

	r, err := resolve(ctx, tx, p)
	if err != nil {
		return 0, 0, fmt.Errorf("catalog: upsert: %w", err)
	}
	if r.removed {
		return 0, OutcomeTombstoned, nil
	}
	matchIDs := r.matchIDs

	// New vacancy.
	if len(matchIDs) == 0 {
		row := vacancyRow{
			identity:       r.identity,
			title:          p.Title,
			company:        r.company,
			url:            r.canonURL,
			applyURL:       r.canonApply,
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
			industries:     c.computeIndustries(p.Title, tagsJSON, newBody),
		}
		if p.Requested {
			row.detailRequestedAt = nowStr
		}
		id, err := save(ctx, tx, row)
		if err != nil {
			return 0, 0, fmt.Errorf("catalog: upsert: insert: %w", err)
		}
		if err := WriteFTS(ctx, tx, id, row.title, row.tagsJSON, newBody); err != nil {
			return 0, 0, fmt.Errorf("catalog: upsert: fts: %w", err)
		}
		if err := saveSource(ctx, tx, id, src, r.canonURL, nowStr); err != nil {
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
	ex, err := loadVacancy(ctx, tx, vacancyID)
	if err != nil {
		return 0, 0, fmt.Errorf("catalog: upsert: load: %w", err)
	}

	// Apply update rules.
	upd := ex // start from existing
	if p.Requested {
		upd.detailRequestedAt = nowStr
	}

	upd.title = p.Title // always from posting
	// A guessed company (derived from the board's address) does not replace a
	// stored company that is already named; it may replace an empty company.
	if r.company != "" && (!p.CompanyGuessed || ex.company == "") {
		upd.company = r.company
		upd.identity = listing.Identity(r.company, p.Title)
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
	if ex.applyURL == "" && r.canonApply != "" {
		upd.applyURL = r.canonApply
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
	// Allow the state to advance to 'blocked' when the vacancy has no stored
	// text. A vacancy that already has full or summary text keeps it.
	exHasText := listing.TextState(ex.textState) == listing.TextFull ||
		listing.TextState(ex.textState) == listing.TextSummary
	if !textChanged && p.TextState == listing.TextBlocked && !exHasText {
		upd.textState = textState
	}
	upd.lastSeen = nowStr

	// Reassign industries when title, tags or text changed. A vacancy whose
	// text was removed by the retention job is never updated via Upsert, so
	// its industries are preserved naturally.
	titleChanged := p.Title != ex.title
	tagsChanged := len(p.Tags) > 0 && tagsJSON != ex.tagsJSON
	if titleChanged || tagsChanged || textChanged {
		upd.industries = c.computeIndustries(upd.title, upd.tagsJSON, upd.body)
	}

	if _, err := save(ctx, tx, upd); err != nil {
		return 0, 0, fmt.Errorf("catalog: upsert: update: %w", err)
	}

	if titleChanged || tagsChanged || textChanged {
		if err := WriteFTS(ctx, tx, vacancyID, upd.title, upd.tagsJSON, upd.body); err != nil {
			return 0, 0, fmt.Errorf("catalog: upsert: fts: %w", err)
		}
	}
	if err := saveSource(ctx, tx, vacancyID, src, r.canonURL, nowStr); err != nil {
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

type vacancyRow struct {
	id                int64
	identity          string
	title             string
	company           string
	url               string
	applyURL          string
	location          string
	tagsJSON          string
	fieldsJSON        string
	publishedAt       string
	publishedBasis    string
	expiresAt         string
	firstSeen         string
	lastSeen          string
	seenAt            string
	textState         string
	contentHash       string
	version           int
	body              []byte
	detailRequestedAt string
	industries        string // JSON array of industry names
}

// save inserts or updates a vacancy row. When v.id is 0 an INSERT is performed
// and the new id is returned. Otherwise an UPDATE is performed and v.id is returned.
func save(ctx context.Context, tx *sql.Tx, v vacancyRow) (int64, error) {
	if v.body == nil {
		v.body = []byte{}
	}
	if v.industries == "" {
		v.industries = "[]"
	}
	if v.id == 0 {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO vacancy
				(identity, title, company, url, apply_url, location, tags, source_fields,
				 published_at, published_basis, expires_at,
				 first_seen, last_seen, text_state, content_hash, version, body,
				 detail_requested_at, industries)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			v.identity, v.title, v.company, v.url, v.applyURL, v.location,
			v.tagsJSON, v.fieldsJSON,
			v.publishedAt, v.publishedBasis, v.expiresAt,
			v.firstSeen, v.lastSeen, v.textState, v.contentHash, v.version, v.body,
			v.detailRequestedAt, v.industries)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE vacancy SET
			identity=?, title=?, company=?, location=?, tags=?, source_fields=?,
			apply_url=?, published_at=?, published_basis=?, expires_at=?,
			last_seen=?, seen_at=?, text_state=?, content_hash=?, version=?, body=?,
			detail_requested_at=?, industries=?
		WHERE id=?`,
		v.identity, v.title, v.company, v.location, v.tagsJSON, v.fieldsJSON,
		v.applyURL, v.publishedAt, v.publishedBasis, v.expiresAt,
		v.lastSeen, v.seenAt, v.textState, v.contentHash, v.version, v.body,
		v.detailRequestedAt, v.industries, v.id)
	return v.id, err
}

func loadVacancy(ctx context.Context, tx *sql.Tx, id int64) (vacancyRow, error) {
	var ex vacancyRow
	err := tx.QueryRowContext(ctx, `
		SELECT id, identity, title, company, url, apply_url, location, tags, source_fields,
		       published_at, published_basis, expires_at,
		       first_seen, last_seen, seen_at, text_state, content_hash, version, body,
		       detail_requested_at, industries
		FROM vacancy WHERE id = ?`, id).Scan(
		&ex.id, &ex.identity, &ex.title, &ex.company, &ex.url, &ex.applyURL, &ex.location,
		&ex.tagsJSON, &ex.fieldsJSON,
		&ex.publishedAt, &ex.publishedBasis, &ex.expiresAt,
		&ex.firstSeen, &ex.lastSeen, &ex.seenAt, &ex.textState, &ex.contentHash, &ex.version, &ex.body,
		&ex.detailRequestedAt, &ex.industries)
	return ex, err
}

// saveSource inserts or updates a vacancy_source row. ON CONFLICT updates seen_at.
func saveSource(ctx context.Context, tx *sql.Tx, vacancyID int64, src, u, seenAt string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO vacancy_source (vacancy_id, source, url, seen_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(url) DO UPDATE SET seen_at = excluded.seen_at`,
		vacancyID, src, u, seenAt)
	return err
}

// findMatches returns all vacancy ids that match the posting.
// atsApply is the ConcreteATS form of the apply URL (empty when not concrete).
// canonApply is the best canonical form used for the conflict guard in rule (c).
func findMatches(ctx context.Context, tx *sql.Tx, canonURL, atsApply, canonApply, identity, company string) ([]int64, error) {
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

	// Matches (b) and (c) in one query, filtered in Go.
	// (b): same concrete ATS apply address.
	// (c): same identity, company not empty, different host, no conflicting apply URL.
	newHost := urlHost(canonURL)
	rows, err := tx.QueryContext(ctx, `
		SELECT id, url, apply_url FROM vacancy
		WHERE (? != '' AND apply_url = ?) OR (identity = ? AND company != '')`,
		atsApply, atsApply, identity)
	if err != nil {
		return nil, fmt.Errorf("match by apply/identity: %w", err)
	}
	for rows.Next() {
		var rid int64
		var rURL, rApply string
		if err := rows.Scan(&rid, &rURL, &rApply); err != nil {
			rows.Close()
			return nil, err
		}
		// Match (b): matching apply URL.
		if atsApply != "" && rApply == atsApply {
			ids = appendUnique(ids, rid)
			continue
		}
		// Match (c): different host, company not empty, apply addresses do not
		// conflict. Two apply addresses conflict only when they name different
		// concrete vacancies on the same applicant tracking system; addresses on
		// different systems (e.g. Ashby and Greenhouse) do not conflict.
		if company == "" {
			continue
		}
		if urlHost(rURL) == newHost {
			continue
		}
		if canonApply != "" && rApply != "" && canonApply != rApply && urlHost(canonApply) == urlHost(rApply) {
			continue
		}
		ids = appendUnique(ids, rid)
	}
	if err := rows.Close(); err != nil {
		return nil, err
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
// Runs of whitespace count as one space, so a change of layout alone is not
// a change of text.
func contentHash(plainText string) string {
	plainText = strings.Join(strings.Fields(plainText), " ")
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

// tombstoneKeys returns the tombstone lookup keys for a posting.
// It includes the canonical URL, legacy hash keys and, when the apply address
// is a concrete applicant-tracking-system vacancy, its canonical key too.
func tombstoneKeys(canonURL, rawURL, applyURL string) []string {
	keys := []string{"url:" + canonURL}
	if lk := listing.LegacyKey(rawURL); lk != "" {
		keys = append(keys, "legacy:"+lk)
	}
	if applyURL != "" {
		if lk := listing.LegacyKey(applyURL); lk != "" {
			keys = append(keys, "legacy:"+lk)
		}
		if ca, ok := listing.ConcreteATS(applyURL); ok {
			keys = append(keys, "url:"+ca)
		}
	}
	return keys
}

// dropVacancy removes a vacancy row, its FTS entry and stale alias rows.
// Sources, decisions and applications are removed by CASCADE.
// It is used by both Delete and performMerge.
func dropVacancy(ctx context.Context, tx *sql.Tx, id int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM vacancy_alias WHERE new_id=?`, id); err != nil {
		return fmt.Errorf("catalog: drop: aliases %d: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM vacancy_fts WHERE rowid=?`, id); err != nil {
		return fmt.Errorf("catalog: drop: fts %d: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM vacancy WHERE id=?`, id); err != nil {
		return fmt.Errorf("catalog: drop: vacancy %d: %w", id, err)
	}
	return nil
}

// Delete permanently removes a vacancy and writes tombstone keys so it cannot
// be re-collected. The caller must verify the vacancy is not in Inbox for the
// requesting profile and that no collect job is active.
func (c *Catalog) Delete(ctx context.Context, id int64) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("catalog: delete: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var vacURL, applyURL string
	if err := tx.QueryRowContext(ctx,
		`SELECT url, apply_url FROM vacancy WHERE id=?`, id).Scan(&vacURL, &applyURL); err != nil {
		if err == sql.ErrNoRows {
			return nil // already gone
		}
		return fmt.Errorf("catalog: delete: load: %w", err)
	}

	for _, k := range tombstoneKeys(vacURL, vacURL, applyURL) {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO tombstone (key) VALUES (?)`, k); err != nil {
			return fmt.Errorf("catalog: delete: tombstone: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO tombstone (key) SELECT 'url:'||url FROM vacancy_source WHERE vacancy_id=?`, id); err != nil {
		return fmt.Errorf("catalog: delete: tombstone sources: %w", err)
	}

	if err := dropVacancy(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Known describes the state of a posting as seen by the catalogue.
// When Removed is true the address is on the removed list.
// When New is true no vacancy matched.
// Otherwise HasText and Requested describe the matched vacancy.
type Known struct {
	Removed   bool
	New       bool
	HasText   bool      // matched vacancy has stored text (full or summary)
	Requested time.Time // when detail was last requested; zero if never
}

// Known returns the state of a posting without modifying the catalogue.
// It uses the same matching rules as Upsert.
func (c *Catalog) Known(ctx context.Context, p listing.Posting) (Known, error) {
	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Known{}, fmt.Errorf("catalog: known: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	r, err := resolve(ctx, tx, p)
	if err != nil {
		return Known{}, fmt.Errorf("catalog: known: %w", err)
	}
	if r.removed {
		return Known{Removed: true}, nil
	}
	if len(r.matchIDs) == 0 {
		return Known{New: true}, nil
	}
	sort.Slice(r.matchIDs, func(i, j int) bool { return r.matchIDs[i] < r.matchIDs[j] })
	var textState, requestedAt string
	if err := tx.QueryRowContext(ctx,
		`SELECT text_state, detail_requested_at FROM vacancy WHERE id = ?`, r.matchIDs[0]).
		Scan(&textState, &requestedAt); err != nil {
		return Known{}, fmt.Errorf("catalog: known: load: %w", err)
	}
	hasText := textState == string(listing.TextFull) || textState == string(listing.TextSummary)
	var requested time.Time
	if requestedAt != "" {
		requested, _ = time.Parse(time.RFC3339, requestedAt)
	}
	return Known{HasText: hasText, Requested: requested}, nil
}

// anyTombstoned reports whether any of the given keys appears in the
// tombstone table (one IN query).
func anyTombstoned(ctx context.Context, tx *sql.Tx, keys []string) (bool, error) {
	if len(keys) == 0 {
		return false, nil
	}
	ph := strings.Repeat("?,", len(keys))
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	var n int
	err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM tombstone WHERE key IN (`+ph[:len(ph)-1]+`)`,
		args...).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// performMerge merges all vacancies in matchIDs (sorted low to high) into the
// first (lowest-id) one. Before deletion the kept vacancy receives the best
// data from all matched rows: earliest first_seen; earliest publication date;
// apply URL when it has none; best text (full > summary > none, lowest id wins
// tie; version increments when text is replaced).
// matchIDs must already be sorted ascending.
func performMerge(ctx context.Context, tx *sql.Tx, matchIDs []int64) error {
	rows := make([]vacancyRow, len(matchIDs))
	for i, id := range matchIDs {
		r, err := loadVacancy(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("load vacancy %d: %w", id, err)
		}
		rows[i] = r
	}

	upd := rows[0] // kept = lowest id
	textChanged := false

	textRank := func(s string) int {
		switch listing.TextState(s) {
		case listing.TextFull:
			return 2
		case listing.TextSummary:
			return 1
		}
		return 0
	}

	for _, r := range rows[1:] {
		upd.firstSeen = pickEarlier(upd.firstSeen, r.firstSeen)
		upd.seenAt = pickEarlier(upd.seenAt, r.seenAt)
		if r.publishedAt != "" && (upd.publishedAt == "" || r.publishedAt < upd.publishedAt) {
			upd.publishedAt = r.publishedAt
			upd.publishedBasis = r.publishedBasis
		}
		if upd.applyURL == "" && r.applyURL != "" {
			upd.applyURL = r.applyURL
		}
		if textRank(r.textState) > textRank(upd.textState) {
			upd.textState = r.textState
			upd.contentHash = r.contentHash
			upd.body = r.body
			upd.version++
			textChanged = true
		}
	}

	// Merge decisions and applications before dropping old vacancies.
	ph := strings.Repeat("?,", len(matchIDs))
	mergeArgs := make([]any, len(matchIDs)+1)
	mergeArgs[0] = upd.id
	for i, id := range matchIDs {
		mergeArgs[i+1] = id
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO decision (profile, vacancy, stage, reason, decided_at)
		SELECT profile, ?, stage, reason, decided_at FROM (
			SELECT *, row_number() OVER (PARTITION BY profile ORDER BY
				CASE stage WHEN 'applied' THEN 3 WHEN 'shortlist' THEN 2 ELSE 1 END DESC,
				decided_at DESC) AS rn
			FROM decision WHERE vacancy IN (`+ph[:len(ph)-1]+`)) WHERE rn=1`,
		mergeArgs...); err != nil {
		return fmt.Errorf("merge decisions: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE application SET vacancy=? WHERE vacancy IN (`+ph[:len(ph)-1]+`)`,
		mergeArgs...); err != nil {
		return fmt.Errorf("move applications: %w", err)
	}

	if _, err := save(ctx, tx, upd); err != nil {
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
		// dropVacancy handles FTS, stale alias rows and the vacancy row (cascade removes decisions/apps/sources).
		if err := dropVacancy(ctx, tx, oldID); err != nil {
			return fmt.Errorf("drop merged %d: %w", oldID, err)
		}
	}
	if textChanged {
		if err := WriteFTS(ctx, tx, upd.id, upd.title, upd.tagsJSON, upd.body); err != nil {
			return fmt.Errorf("fts kept %d: %w", upd.id, err)
		}
	}
	return nil
}

// computeIndustries calls the assigner (if set) with the vacancy's title,
// tags and body text, and returns the JSON-encoded result. When no assigner
// is configured or the assignment returns nothing, "[]" is returned.
func (c *Catalog) computeIndustries(title, tagsJSON string, body []byte) string {
	if c.assign == nil {
		return "[]"
	}
	var tags []string
	json.Unmarshal([]byte(tagsJSON), &tags) //nolint:errcheck
	tagsText := strings.Join(tags, " ")

	var plainText string
	if len(body) > 0 {
		h, err := Body(body)
		if err == nil {
			plainText = listing.PlainText(h)
		}
	}

	names := c.assign(title, tagsText, plainText)
	if len(names) == 0 {
		return "[]"
	}
	b, err := json.Marshal(names)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// SetIndustries updates the industries column of a vacancy directly.
// It is used by the assign_industries background job to reassign all vacancies
// when the vocabulary changes. An empty or nil names slice is stored as "[]",
// never as JSON null.
func (c *Catalog) SetIndustries(ctx context.Context, vacancyID int64, names []string) error {
	_, err := c.db.ExecContext(ctx,
		`UPDATE vacancy SET industries=? WHERE id=?`, marshalTags(names), vacancyID)
	if err != nil {
		return fmt.Errorf("catalog: set industries: %w", err)
	}
	return nil
}

// VacancyForAssign holds the fields needed to reassign industries for one vacancy.
type VacancyForAssign struct {
	ID       int64
	Title    string
	TagsJSON string
	Body     []byte
}

// VacanciesForAssign returns a page of vacancies for industry reassignment,
// ordered by id. offset and limit control pagination.
func (c *Catalog) VacanciesForAssign(ctx context.Context, offset, limit int) ([]VacancyForAssign, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, title, tags, body FROM vacancy ORDER BY id LIMIT ? OFFSET ?`,
		limit, offset)
	if err != nil {
		return nil, fmt.Errorf("catalog: vacancies for assign: %w", err)
	}
	defer rows.Close()
	var out []VacancyForAssign
	for rows.Next() {
		var r VacancyForAssign
		if err := rows.Scan(&r.ID, &r.Title, &r.TagsJSON, &r.Body); err != nil {
			return nil, fmt.Errorf("catalog: vacancies for assign: scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountVacancies returns the total number of vacancies.
func (c *Catalog) CountVacancies(ctx context.Context) (int, error) {
	var n int
	err := c.db.QueryRowContext(ctx, `SELECT count(*) FROM vacancy`).Scan(&n)
	return n, err
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
