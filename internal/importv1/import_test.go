package importv1_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sifty-works/sifty/internal/catalog"
	"github.com/sifty-works/sifty/internal/config"
	"github.com/sifty-works/sifty/internal/db"
	"github.com/sifty-works/sifty/internal/importv1"
	"github.com/sifty-works/sifty/internal/listing"
	_ "modernc.org/sqlite"
)

// oldSchema is the schema of the previous application's database.
const oldSchema = `
CREATE TABLE jobs(
  id INTEGER PRIMARY KEY,
  identity TEXT NOT NULL,
  title TEXT NOT NULL,
  company TEXT NOT NULL,
  original_url TEXT NOT NULL,
  apply_url TEXT,
  description TEXT NOT NULL,
  description_status TEXT NOT NULL,
  location TEXT,
  published_at TEXT,
  expires_at TEXT,
  status TEXT NOT NULL DEFAULT 'new',
  first_seen TEXT NOT NULL,
  last_seen TEXT NOT NULL,
  assessment TEXT,
  assessment_error TEXT,
  apply_status TEXT NOT NULL DEFAULT 'not resolved',
  tags TEXT NOT NULL DEFAULT '[]',
  seen_at TEXT,
  applied_at TEXT,
  match_score INTEGER NOT NULL DEFAULT 0,
  match_profile_revision INTEGER NOT NULL DEFAULT 0,
  outcome_reason TEXT,
  published_basis TEXT,
  description_html TEXT,
  needs_refetch INTEGER NOT NULL DEFAULT 0,
  employer_found_at TEXT
);
CREATE TABLE job_aliases(old_id INTEGER PRIMARY KEY, new_id INTEGER NOT NULL);
CREATE TABLE deleted_listings(hash TEXT PRIMARY KEY);
CREATE TABLE sightings(
  job_id INTEGER NOT NULL REFERENCES jobs(id),
  source TEXT NOT NULL,
  url TEXT NOT NULL UNIQUE,
  seen TEXT NOT NULL
);
CREATE TABLE id_sequence(name TEXT PRIMARY KEY, value INTEGER NOT NULL);
CREATE TABLE collection_runs(
  id INTEGER PRIMARY KEY,
  started TEXT NOT NULL,
  finished TEXT,
  status TEXT NOT NULL,
  pid INTEGER NOT NULL,
  mode TEXT NOT NULL,
  config_hash TEXT NOT NULL
);
`

// makeOldDB creates a fresh old-schema SQLite database at path and sets its
// user_version to v.
func makeOldDB(t *testing.T, dir, name string, v int) *sql.DB {
	t.Helper()
	path := filepath.Join(dir, name)
	oldDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open old db: %v", err)
	}
	t.Cleanup(func() { oldDB.Close() })
	if _, err := oldDB.Exec(oldSchema); err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	if _, err := oldDB.Exec(fmt.Sprintf("PRAGMA user_version = %d", v)); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	return oldDB
}

// makeOldDir creates the layout of the old application's data folder:
// store.sqlite and config/search.yaml with given YAML content.
func makeOldDir(t *testing.T, yamlContent string) string {
	t.Helper()
	dir := t.TempDir()
	oldDB := makeOldDB(t, dir, oldDBFile, 43)
	_ = oldDB // returned for callers that want to insert rows

	cfgDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if yamlContent != "" {
		if err := os.WriteFile(filepath.Join(cfgDir, "search.yaml"), []byte(yamlContent), 0600); err != nil {
			t.Fatalf("write search.yaml: %v", err)
		}
	}
	return dir
}

// openOldDB opens the old database in a test-created directory for adding rows.
func openOldDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	path := filepath.Join(dir, oldDBFile)
	oldDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open old db for writing: %v", err)
	}
	t.Cleanup(func() { oldDB.Close() })
	return oldDB
}

const oldDBFile = "store.sqlite"

// runImport runs the import and returns the stdout text.
func runImport(t *testing.T, fromDir, targetDir string) string {
	t.Helper()
	dbPath := filepath.Join(targetDir, "sifty.db")
	settingsPath := filepath.Join(targetDir, "sifty.yaml")
	var buf bytes.Buffer
	if err := importv1.Import(context.Background(), fromDir, dbPath, settingsPath, &buf); err != nil {
		t.Fatalf("import: %v", err)
	}
	return buf.String()
}

// openNewDB opens the new database in targetDir.
func openNewDB(t *testing.T, targetDir string) *db.DB {
	t.Helper()
	newDB, err := db.Open(filepath.Join(targetDir, "sifty.db"), db.Migrations)
	if err != nil {
		t.Fatalf("open new db: %v", err)
	}
	t.Cleanup(func() { newDB.Close() })
	return newDB
}

// --- Tests ---

func TestImport_AllFieldsCarriedOver(t *testing.T) {
	fromDir := makeOldDir(t, `
version: 1
sites:
  - name: Acme Board
    url: https://boards.greenhouse.io/acmeco
    enabled: true
search: {}
preferences: {}
`)
	oldDB := openOldDB(t, fromDir)

	// Insert one full vacancy with HTML description and a sighting.
	_, err := oldDB.Exec(`
		INSERT INTO jobs (id, identity, title, company, original_url, apply_url,
		  description, description_status, location, published_at, published_basis,
		  expires_at, first_seen, last_seen, tags, description_html)
		VALUES (1, 'acme|engineer', 'Senior Engineer', 'Acme Corp',
		  'https://boards.greenhouse.io/acmeco/jobs/42',
		  'https://boards.greenhouse.io/acmeco/jobs/42',
		  '', 'available',
		  'Remote', '2024-01-10T09:00:00Z', 'datePosted',
		  '2024-06-01T00:00:00Z',
		  '2024-01-10T09:00:00Z', '2024-01-15T12:00:00Z',
		  '["backend","go"]',
		  '<p>Full job description.</p>')`)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
	_, err = oldDB.Exec(`
		INSERT INTO sightings (job_id, source, url, seen)
		VALUES (1, 'Acme Board', 'https://boards.greenhouse.io/acmeco/jobs/42', '2024-01-10T09:00:00Z')`)
	if err != nil {
		t.Fatalf("insert sighting: %v", err)
	}

	targetDir := t.TempDir()
	runImport(t, fromDir, targetDir)
	newDB := openNewDB(t, targetDir)

	var id int64
	var title, company, url, applyURL, location, tags, identity string
	var publishedAt, publishedBasis, expiresAt, firstSeen, lastSeen string
	var textState, contentHash string
	var version int
	err = newDB.QueryRow(`
		SELECT id, identity, title, company, url, apply_url, location, tags,
		       published_at, published_basis, expires_at,
		       first_seen, last_seen, text_state, content_hash, version
		FROM vacancy WHERE id=1`).Scan(
		&id, &identity, &title, &company, &url, &applyURL, &location, &tags,
		&publishedAt, &publishedBasis, &expiresAt,
		&firstSeen, &lastSeen, &textState, &contentHash, &version)
	if err != nil {
		t.Fatalf("query vacancy: %v", err)
	}

	if title != "Senior Engineer" {
		t.Errorf("title: got %q, want %q", title, "Senior Engineer")
	}
	if company != "Acme Corp" {
		t.Errorf("company: got %q, want %q", company, "Acme Corp")
	}
	// Canonical URL strips www, lowercase, etc.
	if !strings.HasPrefix(url, "https://boards.greenhouse.io") {
		t.Errorf("url: got %q", url)
	}
	if applyURL == "" {
		t.Errorf("apply_url: empty, want concrete ATS address")
	}
	if location != "Remote" {
		t.Errorf("location: got %q", location)
	}
	if tags != `["backend","go"]` {
		t.Errorf("tags: got %q", tags)
	}
	if publishedAt != "2024-01-10T09:00:00Z" {
		t.Errorf("published_at: got %q", publishedAt)
	}
	if publishedBasis != "datePosted" {
		t.Errorf("published_basis: got %q", publishedBasis)
	}
	if expiresAt != "2024-06-01T00:00:00Z" {
		t.Errorf("expires_at: got %q", expiresAt)
	}
	if firstSeen != "2024-01-10T09:00:00Z" {
		t.Errorf("first_seen: got %q", firstSeen)
	}
	if lastSeen != "2024-01-15T12:00:00Z" {
		t.Errorf("last_seen: got %q", lastSeen)
	}
	if textState != "full" {
		t.Errorf("text_state: got %q, want full", textState)
	}
	if contentHash == "" {
		t.Errorf("content_hash: empty")
	}
	if version != 1 {
		t.Errorf("version: got %d, want 1", version)
	}
	wantIdentity := listing.Identity("Acme Corp", "Senior Engineer")
	if identity != wantIdentity {
		t.Errorf("identity: got %q, want %q", identity, wantIdentity)
	}
}

func TestImport_TextStates(t *testing.T) {
	cases := []struct {
		status    string
		desc      string
		descHTML  string
		wantState string
	}{
		{"blocked_by_site", "", "", "blocked"},
		{"blocked_something", "some text", "", "blocked"},
		{"available", "<p>full text</p>", "", "full"},
		{"available_full", "", "<p>html</p>", "full"},
		{"other_status", "some text", "", "summary"},
		{"", "", "", "not_fetched"},
		{"pending", "", "", "not_fetched"},
	}

	fromDir := makeOldDir(t, "")
	oldDB := openOldDB(t, fromDir)

	for i, tc := range cases {
		descHTML := "NULL"
		descHTMLVal := interface{}(nil)
		if tc.descHTML != "" {
			descHTMLVal = tc.descHTML
		}
		_, err := oldDB.Exec(`
			INSERT INTO jobs (id, identity, title, company, original_url,
			  description, description_status, first_seen, last_seen, description_html)
			VALUES (?, 'co|title', 'Title', 'Co',
			  ?,
			  ?, ?, '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z', ?)`,
			i+1,
			fmt.Sprintf("https://example.com/jobs/%d", i+1),
			tc.desc, tc.status, descHTMLVal,
		)
		_ = descHTML
		if err != nil {
			t.Fatalf("insert job %d: %v", i+1, err)
		}
	}

	targetDir := t.TempDir()
	runImport(t, fromDir, targetDir)
	newDB := openNewDB(t, targetDir)

	for i, tc := range cases {
		var state string
		err := newDB.QueryRow(`SELECT text_state FROM vacancy WHERE id=?`, i+1).Scan(&state)
		if err != nil {
			t.Errorf("case %d (%q): query: %v", i, tc.status, err)
			continue
		}
		if state != tc.wantState {
			t.Errorf("case %d status=%q: got state %q, want %q", i, tc.status, state, tc.wantState)
		}
	}
}

func TestImport_PlainTextDescription(t *testing.T) {
	fromDir := makeOldDir(t, "")
	oldDB := openOldDB(t, fromDir)

	plainText := "Requirements\n- Go experience\n- SQL knowledge"
	_, err := oldDB.Exec(`
		INSERT INTO jobs (id, identity, title, company, original_url,
		  description, description_status, first_seen, last_seen)
		VALUES (1, 'co|t', 'Title', 'Co', 'https://example.com/jobs/1',
		  ?, 'available', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
		plainText)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	targetDir := t.TempDir()
	runImport(t, fromDir, targetDir)
	newDB := openNewDB(t, targetDir)

	var body []byte
	var hash string
	err = newDB.QueryRow(`SELECT body, content_hash FROM vacancy WHERE id=1`).Scan(&body, &hash)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	// Verify that catalog.Encode of the same normalized text gives identical body and hash.
	normalized := listing.Normalize(plainText, listing.Plain)
	wantBody, wantHash, err := catalog.Encode(normalized)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if hash != wantHash {
		t.Errorf("hash: got %q, want %q", hash, wantHash)
	}
	if !bytes.Equal(body, wantBody) {
		t.Errorf("body: mismatch")
	}
}

func TestImport_EncodeConsistencyWithUpsert(t *testing.T) {
	// A later Upsert of the same text should return Unchanged.
	fromDir := makeOldDir(t, "")
	oldDB := openOldDB(t, fromDir)

	htmlText := "<p>We are hiring a Go engineer to work on <strong>interesting</strong> systems.</p>"
	_, err := oldDB.Exec(`
		INSERT INTO jobs (id, identity, title, company, original_url,
		  description, description_status, first_seen, last_seen, description_html)
		VALUES (1, 'alpha|engineer', 'Engineer', 'Alpha',
		  'https://example.com/jobs/1',
		  '', 'available', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z', ?)`,
		htmlText)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	_, err = oldDB.Exec(`
		INSERT INTO sightings (job_id, source, url, seen)
		VALUES (1, 'alpha', 'https://example.com/jobs/1', '2024-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert sighting: %v", err)
	}

	targetDir := t.TempDir()
	runImport(t, fromDir, targetDir)
	newDB := openNewDB(t, targetDir)

	cat := catalog.New(newDB, time.Now)
	p := listing.Posting{
		URL:       "https://example.com/jobs/1",
		Title:     "Engineer",
		Company:   "Alpha",
		Text:      htmlText,
		TextKind:  listing.HTML,
		TextState: listing.TextFull,
	}
	_, outcome, err := cat.Upsert(context.Background(), p, "alpha")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if outcome != catalog.OutcomeUnchanged {
		t.Errorf("upsert outcome: got %v, want Unchanged", outcome)
	}
}

func TestImport_PublicationRules(t *testing.T) {
	fromDir := makeOldDir(t, "")
	oldDB := openOldDB(t, fromDir)

	future := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	_, err := oldDB.Exec(`
		INSERT INTO jobs (id, identity, title, company, original_url,
		  description, description_status, published_at, published_basis,
		  first_seen, last_seen)
		VALUES
		  (1,'co|a','A','Co','https://example.com/jobs/1','t','available','2024-03-15T00:00:00Z','datePosted','2024-03-15T00:00:00Z','2024-03-15T00:00:00Z'),
		  (2,'co|b','B','Co','https://example.com/jobs/2','t','available','2024-03-15T00:00:00Z','pubDate','2024-03-15T00:00:00Z','2024-03-15T00:00:00Z'),
		  (3,'co|c','C','Co','https://example.com/jobs/3','t','available','2024-03-15T00:00:00Z','updatedAt','2024-03-15T00:00:00Z','2024-03-15T00:00:00Z'),
		  (4,'co|d','D','Co','https://example.com/jobs/4','t','available',?,'datePosted','2024-03-15T00:00:00Z','2024-03-15T00:00:00Z'),
		  (5,'co|e','E','Co','https://example.com/jobs/5','t','available','not-a-date','datePosted','2024-03-15T00:00:00Z','2024-03-15T00:00:00Z'),
		  (6,'co|f','F','Co','https://example.com/jobs/6','t','available','2024-03-15T00:00:00Z',NULL,'2024-03-15T00:00:00Z','2024-03-15T00:00:00Z')`,
		future)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	targetDir := t.TempDir()
	runImport(t, fromDir, targetDir)
	newDB := openNewDB(t, targetDir)

	type row struct {
		publishedAt    string
		publishedBasis string
	}
	read := func(id int) row {
		var r row
		newDB.QueryRow(`SELECT published_at, published_basis FROM vacancy WHERE id=?`, id).Scan(&r.publishedAt, &r.publishedBasis) //nolint:errcheck
		return r
	}

	r1 := read(1) // datePosted → keep with basis
	if r1.publishedAt != "2024-03-15T00:00:00Z" || r1.publishedBasis != "datePosted" {
		t.Errorf("id=1: got published_at=%q basis=%q", r1.publishedAt, r1.publishedBasis)
	}
	r2 := read(2) // pubDate → keep with basis
	if r2.publishedAt != "2024-03-15T00:00:00Z" || r2.publishedBasis != "pubDate" {
		t.Errorf("id=2: got published_at=%q basis=%q", r2.publishedAt, r2.publishedBasis)
	}
	r3 := read(3) // updatedAt → keep date without basis
	if r3.publishedAt != "2024-03-15T00:00:00Z" || r3.publishedBasis != "" {
		t.Errorf("id=3: got published_at=%q basis=%q", r3.publishedAt, r3.publishedBasis)
	}
	r4 := read(4) // future date → dropped
	if r4.publishedAt != "" {
		t.Errorf("id=4: future date not dropped; got %q", r4.publishedAt)
	}
	r5 := read(5) // bad date → dropped
	if r5.publishedAt != "" {
		t.Errorf("id=5: bad date not dropped; got %q", r5.publishedAt)
	}
	r6 := read(6) // NULL basis → keep date without basis
	if r6.publishedAt != "2024-03-15T00:00:00Z" || r6.publishedBasis != "" {
		t.Errorf("id=6: got published_at=%q basis=%q", r6.publishedAt, r6.publishedBasis)
	}
}

func TestImport_EmptyCompanyBecomesUnknown(t *testing.T) {
	fromDir := makeOldDir(t, "")
	oldDB := openOldDB(t, fromDir)

	_, err := oldDB.Exec(`
		INSERT INTO jobs (id, identity, title, company, original_url,
		  description, description_status, first_seen, last_seen)
		VALUES (1, '|engineer', 'Engineer', '', 'https://example.com/jobs/1',
		  't', 'available', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	targetDir := t.TempDir()
	runImport(t, fromDir, targetDir)
	newDB := openNewDB(t, targetDir)

	var company string
	newDB.QueryRow(`SELECT company FROM vacancy WHERE id=1`).Scan(&company) //nolint:errcheck
	if company != "Unknown" {
		t.Errorf("company: got %q, want Unknown", company)
	}
}

func TestImport_ApplyURLKeptOnlyConcrete(t *testing.T) {
	fromDir := makeOldDir(t, "")
	oldDB := openOldDB(t, fromDir)

	_, err := oldDB.Exec(`
		INSERT INTO jobs (id, identity, title, company, original_url, apply_url,
		  description, description_status, first_seen, last_seen)
		VALUES
		  (1,'co|a','A','Co','https://example.com/jobs/1',
		   'https://boards.greenhouse.io/acme/jobs/123',
		   't','available','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z'),
		  (2,'co|b','B','Co','https://example.com/jobs/2',
		   'https://boards.greenhouse.io/acme',
		   't','available','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	targetDir := t.TempDir()
	runImport(t, fromDir, targetDir)
	newDB := openNewDB(t, targetDir)

	var apply1, apply2 string
	newDB.QueryRow(`SELECT apply_url FROM vacancy WHERE id=1`).Scan(&apply1) //nolint:errcheck
	newDB.QueryRow(`SELECT apply_url FROM vacancy WHERE id=2`).Scan(&apply2) //nolint:errcheck
	if apply1 == "" {
		t.Errorf("id=1 apply_url: empty, want concrete ATS")
	}
	if apply2 != "" {
		t.Errorf("id=2 apply_url: got %q, want empty (board root)", apply2)
	}
}

func TestImport_DuplicateCanonicalAddress(t *testing.T) {
	fromDir := makeOldDir(t, "")
	oldDB := openOldDB(t, fromDir)

	// Two jobs with URLs that canonicalise to the same address.
	_, err := oldDB.Exec(`
		INSERT INTO jobs (id, identity, title, company, original_url,
		  description, description_status, first_seen, last_seen)
		VALUES
		  (1,'co|eng','Engineer','Co','https://example.com/jobs/10?utm_source=gh',
		   '<p>full text</p>','available','2024-01-01T00:00:00Z','2024-01-05T00:00:00Z'),
		  (2,'co|eng','Engineer','Co','https://example.com/jobs/10?utm_medium=x',
		   't','available','2024-01-02T00:00:00Z','2024-01-10T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	targetDir := t.TempDir()
	report := runImport(t, fromDir, targetDir)
	newDB := openNewDB(t, targetDir)

	// Only one vacancy should be written.
	var n int
	newDB.QueryRow(`SELECT count(*) FROM vacancy`).Scan(&n) //nolint:errcheck
	if n != 1 {
		t.Errorf("vacancy count: got %d, want 1", n)
	}

	// Lower id (1) is kept; higher id (2) becomes an alias.
	var keptID, aliasedID int64
	newDB.QueryRow(`SELECT id FROM vacancy`).Scan(&keptID) //nolint:errcheck
	if keptID != 1 {
		t.Errorf("kept id: got %d, want 1", keptID)
	}
	newDB.QueryRow(`SELECT old_id FROM vacancy_alias WHERE new_id=1`).Scan(&aliasedID) //nolint:errcheck
	if aliasedID != 2 {
		t.Errorf("aliased id: got %d, want 2", aliasedID)
	}

	// Report lists the duplicate pair.
	if !strings.Contains(report, "kept=1 aliased=2") {
		t.Errorf("report missing duplicate pair: %s", report)
	}

	// Merge: last_seen should be the latest (from id=2).
	var lastSeen string
	newDB.QueryRow(`SELECT last_seen FROM vacancy WHERE id=1`).Scan(&lastSeen) //nolint:errcheck
	if lastSeen != "2024-01-10T00:00:00Z" {
		t.Errorf("last_seen: got %q, want 2024-01-10T00:00:00Z", lastSeen)
	}
}

func TestImport_SightingSourceMapping(t *testing.T) {
	fromDir := makeOldDir(t, `
version: 1
sites:
  - name: Greenhouse Acme
    url: https://boards.greenhouse.io/acmeco
    enabled: true
`)
	oldDB := openOldDB(t, fromDir)

	_, err := oldDB.Exec(`
		INSERT INTO jobs (id, identity, title, company, original_url,
		  description, description_status, first_seen, last_seen)
		VALUES
		  (1,'acme|eng','Eng','Acme','https://boards.greenhouse.io/acmeco/jobs/1',
		   't','available','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z'),
		  (2,'other|eng','Eng','Other','https://example.org/jobs/2',
		   't','available','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert jobs: %v", err)
	}
	_, err = oldDB.Exec(`
		INSERT INTO sightings (job_id, source, url, seen)
		VALUES
		  (1, 'Greenhouse Acme', 'https://boards.greenhouse.io/acmeco/jobs/1', '2024-01-01T00:00:00Z'),
		  (2, 'Unknown Source', 'https://example.org/jobs/2', '2024-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert sightings: %v", err)
	}

	targetDir := t.TempDir()
	runImport(t, fromDir, targetDir)
	newDB := openNewDB(t, targetDir)

	// Source for id=1 should be the SourceKey of the config URL.
	expectedKey, _ := config.SourceKey("https://boards.greenhouse.io/acmeco")
	var src1 string
	newDB.QueryRow(`SELECT source FROM vacancy_source WHERE vacancy_id=1`).Scan(&src1) //nolint:errcheck
	if src1 != expectedKey {
		t.Errorf("id=1 source: got %q, want %q (SourceKey)", src1, expectedKey)
	}

	// Source for id=2 should be "name:Unknown Source".
	var src2 string
	newDB.QueryRow(`SELECT source FROM vacancy_source WHERE vacancy_id=2`).Scan(&src2) //nolint:errcheck
	if src2 != "name:Unknown Source" {
		t.Errorf("id=2 source: got %q, want name:Unknown Source", src2)
	}
}

func TestImport_OwnAddressRowInsertedFirst(t *testing.T) {
	fromDir := makeOldDir(t, "")
	oldDB := openOldDB(t, fromDir)

	_, err := oldDB.Exec(`
		INSERT INTO jobs (id, identity, title, company, original_url,
		  description, description_status, first_seen, last_seen)
		VALUES (1,'co|t','T','Co','https://example.com/jobs/1',
		  't','available','2024-01-05T00:00:00Z','2024-01-05T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
	// No sightings — own-address row uses "name:import".
	targetDir := t.TempDir()
	runImport(t, fromDir, targetDir)
	newDB := openNewDB(t, targetDir)

	var src, url, seenAt string
	err = newDB.QueryRow(`SELECT source, url, seen_at FROM vacancy_source WHERE vacancy_id=1`).Scan(&src, &url, &seenAt)
	if err != nil {
		t.Fatalf("query source: %v", err)
	}
	if src != "name:import" {
		t.Errorf("source: got %q, want name:import", src)
	}
	// seenAt should be last_seen of the vacancy.
	if seenAt != "2024-01-05T00:00:00Z" {
		t.Errorf("seen_at: got %q", seenAt)
	}
}

func TestImport_SightingsDuplicateCanonicalSkipped(t *testing.T) {
	fromDir := makeOldDir(t, "")
	oldDB := openOldDB(t, fromDir)

	_, err := oldDB.Exec(`
		INSERT INTO jobs (id, identity, title, company, original_url,
		  description, description_status, first_seen, last_seen)
		VALUES (1,'co|t','T','Co','https://example.com/jobs/1',
		  't','available','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
	// Two sightings with URLs that canonicalise to the same address.
	_, err = oldDB.Exec(`
		INSERT INTO sightings (job_id, source, url, seen)
		VALUES
		  (1, 's1', 'https://example.com/jobs/1?utm_source=a', '2024-01-01T00:00:00Z'),
		  (1, 's2', 'https://example.com/jobs/1?utm_source=b', '2024-01-02T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert sightings: %v", err)
	}

	targetDir := t.TempDir()
	report := runImport(t, fromDir, targetDir)
	newDB := openNewDB(t, targetDir)

	var n int
	newDB.QueryRow(`SELECT count(*) FROM vacancy_source WHERE vacancy_id=1`).Scan(&n) //nolint:errcheck
	// Only the own-address row (which is the canonical URL = same as the sightings) + 0 duplicates.
	// The sightings both canonicalise to the same URL as the vacancy, so only one row.
	if n != 1 {
		t.Errorf("vacancy_source count: got %d, want 1", n)
	}
	if !strings.Contains(report, "skipped") {
		t.Errorf("report should mention skipped sightings")
	}
}

func TestImport_AliasesFollowed(t *testing.T) {
	fromDir := makeOldDir(t, "")
	oldDB := openOldDB(t, fromDir)

	// Job 10 is a live vacancy; jobs 20 and 30 are aliases.
	_, err := oldDB.Exec(`
		INSERT INTO jobs (id, identity, title, company, original_url,
		  description, description_status, first_seen, last_seen)
		VALUES (10,'co|t','T','Co','https://example.com/jobs/10',
		  't','available','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
	// 20 → 10 (direct alias to imported vacancy).
	// 30 → 20 → 10 (chain through another alias).
	_, err = oldDB.Exec(`INSERT INTO job_aliases (old_id, new_id) VALUES (20, 10), (30, 20)`)
	if err != nil {
		t.Fatalf("insert aliases: %v", err)
	}

	targetDir := t.TempDir()
	runImport(t, fromDir, targetDir)
	newDB := openNewDB(t, targetDir)

	// Both 20 and 30 should point to 10.
	var target20, target30 int64
	newDB.QueryRow(`SELECT new_id FROM vacancy_alias WHERE old_id=20`).Scan(&target20) //nolint:errcheck
	newDB.QueryRow(`SELECT new_id FROM vacancy_alias WHERE old_id=30`).Scan(&target30) //nolint:errcheck
	if target20 != 10 {
		t.Errorf("alias 20→: got %d, want 10", target20)
	}
	if target30 != 10 {
		t.Errorf("alias 30→: got %d, want 10", target30)
	}
}

func TestImport_AliasesBadCasesDropped(t *testing.T) {
	fromDir := makeOldDir(t, "")
	oldDB := openOldDB(t, fromDir)

	_, err := oldDB.Exec(`
		INSERT INTO jobs (id, identity, title, company, original_url,
		  description, description_status, first_seen, last_seen)
		VALUES (10,'co|t','T','Co','https://example.com/jobs/10',
		  't','available','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
	// 20 → 999 (dangling: 999 not imported and not aliased).
	// 10 → 20 → 10 would be a loop — but 10 IS in keepIDs.
	// 50 → 10 is old_id=50 and 50 is a kept vacancy... actually let's test old_id = imported vacancy.
	_, err = oldDB.Exec(`
		INSERT INTO job_aliases (old_id, new_id) VALUES
		  (20, 999),
		  (10, 20)`)
	if err != nil {
		t.Fatalf("insert aliases: %v", err)
	}

	targetDir := t.TempDir()
	report := runImport(t, fromDir, targetDir)

	// alias 20 → 999 is dangling (999 not imported).
	if !strings.Contains(report, "dropped alias id=20") {
		t.Errorf("report missing dangling alias drop: %s", report)
	}
	// alias 10 → 20: old_id=10 is an imported vacancy, should be dropped.
	if !strings.Contains(report, "dropped alias id=10") {
		t.Errorf("report missing self-colliding alias drop: %s", report)
	}
}

func TestImport_LoopingAlias(t *testing.T) {
	fromDir := makeOldDir(t, "")
	oldDB := openOldDB(t, fromDir)

	_, err := oldDB.Exec(`
		INSERT INTO jobs (id, identity, title, company, original_url,
		  description, description_status, first_seen, last_seen)
		VALUES (10,'co|t','T','Co','https://example.com/jobs/10',
		  't','available','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
	// Create a loop: 20 → 30 → 20 (without 10 or 30 being an imported vacancy).
	_, err = oldDB.Exec(`INSERT INTO job_aliases (old_id, new_id) VALUES (20, 30), (30, 20)`)
	if err != nil {
		t.Fatalf("insert aliases: %v", err)
	}

	targetDir := t.TempDir()
	report := runImport(t, fromDir, targetDir)

	if !strings.Contains(report, "dropped alias id=20") {
		t.Errorf("report missing looping alias drop: %s", report)
	}
}

func TestImport_TombstonesImported(t *testing.T) {
	fromDir := makeOldDir(t, "")
	oldDB := openOldDB(t, fromDir)

	const hash = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	_, err := oldDB.Exec(`INSERT INTO deleted_listings (hash) VALUES (?)`, hash)
	if err != nil {
		t.Fatalf("insert hash: %v", err)
	}

	targetDir := t.TempDir()
	runImport(t, fromDir, targetDir)
	newDB := openNewDB(t, targetDir)

	var key string
	err = newDB.QueryRow(`SELECT key FROM tombstone WHERE key=?`, "legacy:"+hash).Scan(&key)
	if err != nil {
		t.Fatalf("tombstone not found: %v", err)
	}
}

func TestImport_TombstonedAddressBlocksUpsert(t *testing.T) {
	fromDir := makeOldDir(t, "")
	oldDB := openOldDB(t, fromDir)

	// Insert a vacancy whose URL hashes to a known legacy key.
	removedURL := "https://example.com/removed/42"
	legacyHash := listing.LegacyKey(removedURL)
	_, err := oldDB.Exec(`INSERT INTO deleted_listings (hash) VALUES (?)`, legacyHash)
	if err != nil {
		t.Fatalf("insert hash: %v", err)
	}

	targetDir := t.TempDir()
	runImport(t, fromDir, targetDir)
	newDB := openNewDB(t, targetDir)

	cat := catalog.New(newDB, time.Now)
	p := listing.Posting{
		URL:       removedURL,
		Title:     "Removed Job",
		Company:   "Co",
		Text:      "text",
		TextKind:  listing.Plain,
		TextState: listing.TextFull,
	}
	_, outcome, err := cat.Upsert(context.Background(), p, "src")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if outcome != catalog.OutcomeTombstoned {
		t.Errorf("outcome: got %v, want Tombstoned", outcome)
	}
}

func TestImport_SourcesWrittenToSettingsFile(t *testing.T) {
	fromDir := makeOldDir(t, `
version: 1
sites:
  - name: Greenhouse Acme
    url: https://boards.greenhouse.io/acmeco
    enabled: true
  - name: Lever Beta
    url: https://jobs.lever.co/betacorp
    enabled: false
  - name: Never Seen
    url: https://jobs.ashbyhq.com/nosightings
    enabled: true
search: {}
preferences: {}
`)
	oldDB := openOldDB(t, fromDir)

	// Insert vacancies with sightings for Greenhouse Acme and Lever Beta.
	_, err := oldDB.Exec(`
		INSERT INTO jobs (id, identity, title, company, original_url,
		  description, description_status, first_seen, last_seen)
		VALUES
		  (1,'acme|e','E','Acme','https://boards.greenhouse.io/acmeco/jobs/1','t','available','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z'),
		  (2,'beta|e','E','Beta','https://jobs.lever.co/betacorp/abc','t','available','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert jobs: %v", err)
	}
	_, err = oldDB.Exec(`
		INSERT INTO sightings (job_id, source, url, seen)
		VALUES
		  (1, 'Greenhouse Acme', 'https://boards.greenhouse.io/acmeco/jobs/1', '2024-01-01T00:00:00Z'),
		  (2, 'Lever Beta', 'https://jobs.lever.co/betacorp/abc', '2024-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert sightings: %v", err)
	}

	targetDir := t.TempDir()
	report := runImport(t, fromDir, targetDir)

	store, err := config.Open(filepath.Join(targetDir, "sifty.yaml"))
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	cfg := store.Get()

	if len(cfg.Sources) != 2 {
		t.Errorf("sources count: got %d, want 2", len(cfg.Sources))
	}
	if cfg.Sources[0].Name != "Greenhouse Acme" {
		t.Errorf("sources[0]: got %q", cfg.Sources[0].Name)
	}
	if cfg.Sources[1].Name != "Lever Beta" {
		t.Errorf("sources[1]: got %q", cfg.Sources[1].Name)
	}
	if cfg.Sources[1].Enabled {
		t.Errorf("sources[1] enabled: got true, want false")
	}

	// "Never Seen" should be in the dropped list.
	if !strings.Contains(report, "Never Seen") {
		t.Errorf("report missing dropped source: %s", report)
	}
	if !strings.Contains(report, "no vacancy") {
		t.Errorf("report missing 'no vacancy': %s", report)
	}
}

func TestImport_SourcesInvalidAndDuplicate(t *testing.T) {
	fromDir := makeOldDir(t, `
version: 1
sites:
  - name: Valid Source
    url: https://boards.greenhouse.io/valid
    enabled: true
  - name: Bad URL
    url: not-a-url
    enabled: true
  - name: Duplicate
    url: https://boards.greenhouse.io/valid
    enabled: true
search: {}
`)
	oldDB := openOldDB(t, fromDir)

	_, err := oldDB.Exec(`
		INSERT INTO jobs (id, identity, title, company, original_url,
		  description, description_status, first_seen, last_seen)
		VALUES (1,'v|e','E','V','https://boards.greenhouse.io/valid/jobs/1','t','available','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
	_, err = oldDB.Exec(`
		INSERT INTO sightings (job_id, source, url, seen)
		VALUES (1, 'Valid Source', 'https://boards.greenhouse.io/valid/jobs/1', '2024-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert sighting: %v", err)
	}

	targetDir := t.TempDir()
	report := runImport(t, fromDir, targetDir)

	if !strings.Contains(report, `"Bad URL"`) {
		t.Errorf("report missing Bad URL: %s", report)
	}
	if !strings.Contains(report, `"Duplicate"`) {
		t.Errorf("report missing Duplicate: %s", report)
	}

	store, _ := config.Open(filepath.Join(targetDir, "sifty.yaml"))
	cfg := store.Get()
	if len(cfg.Sources) != 1 {
		t.Errorf("sources count: got %d, want 1", len(cfg.Sources))
	}
}

func TestImport_NonImportedConfigKeys(t *testing.T) {
	fromDir := makeOldDir(t, `
version: 1
sites: []
search:
  something: value
preferences:
  another: value
`)

	targetDir := t.TempDir()
	report := runImport(t, fromDir, targetDir)

	if !strings.Contains(report, "not imported") {
		t.Errorf("report missing non-imported keys notice: %s", report)
	}
	if !strings.Contains(report, "preferences") || !strings.Contains(report, "search") {
		t.Errorf("report missing key names: %s", report)
	}
}

func TestImport_LastRunWritten(t *testing.T) {
	fromDir := makeOldDir(t, "")
	oldDB := openOldDB(t, fromDir)

	const startTime = "2024-02-20T08:30:00Z"
	_, err := oldDB.Exec(`
		INSERT INTO collection_runs (id, started, finished, status, pid, mode, config_hash)
		VALUES
		  (1, '2024-02-15T08:00:00Z', '2024-02-15T08:05:00Z', 'done', 1, 'auto', 'abc'),
		  (2, ?, '2024-02-20T08:35:00Z', 'done', 2, 'auto', 'abc')`, startTime)
	if err != nil {
		t.Fatalf("insert runs: %v", err)
	}

	targetDir := t.TempDir()
	report := runImport(t, fromDir, targetDir)
	newDB := openNewDB(t, targetDir)

	var kind, subject, state string
	var attempts int
	var notBefore, created, updated string
	err = newDB.QueryRow(`SELECT kind, subject, state, attempts, not_before, created, updated FROM job WHERE kind='collect'`).Scan(
		&kind, &subject, &state, &attempts, &notBefore, &created, &updated)
	if err != nil {
		t.Fatalf("query job: %v", err)
	}
	if kind != "collect" || state != "done" || attempts != 1 || subject != "" {
		t.Errorf("job fields: kind=%q state=%q attempts=%d subject=%q", kind, state, attempts, subject)
	}
	// The time should match the latest started, without fractions.
	if notBefore != startTime {
		t.Errorf("not_before: got %q, want %q", notBefore, startTime)
	}
	if created != notBefore || updated != notBefore {
		t.Errorf("created/updated mismatch: created=%q updated=%q", created, updated)
	}

	if !strings.Contains(report, "Last run: written") {
		t.Errorf("report: %s", report)
	}
}

func TestImport_NextIDAboveAll(t *testing.T) {
	fromDir := makeOldDir(t, "")
	oldDB := openOldDB(t, fromDir)

	// Job IDs: 5, 15; alias old_id: 25; id_sequence value: 50 (highest used = 49).
	_, err := oldDB.Exec(`
		INSERT INTO jobs (id, identity, title, company, original_url,
		  description, description_status, first_seen, last_seen)
		VALUES
		  (5,'co|a','A','Co','https://example.com/jobs/5','t','available','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z'),
		  (15,'co|b','B','Co','https://example.com/jobs/15','t','available','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert jobs: %v", err)
	}
	_, err = oldDB.Exec(`INSERT INTO job_aliases (old_id, new_id) VALUES (25, 15)`)
	if err != nil {
		t.Fatalf("insert alias: %v", err)
	}
	_, err = oldDB.Exec(`INSERT INTO id_sequence (name, value) VALUES ('jobs', 50)`)
	if err != nil {
		t.Fatalf("insert id_sequence: %v", err)
	}

	targetDir := t.TempDir()
	runImport(t, fromDir, targetDir)
	newDB := openNewDB(t, targetDir)

	// Insert a new vacancy to get the next autoincrement id.
	cat := catalog.New(newDB, time.Now)
	p := listing.Posting{
		URL:       "https://example.com/jobs/new",
		Title:     "New Job",
		Company:   "NewCo",
		Text:      "text",
		TextKind:  listing.Plain,
		TextState: listing.TextFull,
	}
	newID, _, err := cat.Upsert(context.Background(), p, "src")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Max of 15, 25, 49 = 49; next id should be > 49.
	if newID <= 49 {
		t.Errorf("next id: got %d, want > 49", newID)
	}
}

func TestImport_UnknownVersionRefused(t *testing.T) {
	dir := t.TempDir()
	oldDB := makeOldDB(t, dir, oldDBFile, 42) // wrong version
	_ = oldDB

	cfgDir := filepath.Join(dir, "config")
	os.MkdirAll(cfgDir, 0700) //nolint:errcheck

	targetDir := filepath.Join(t.TempDir(), "new-data")
	dbPath := filepath.Join(targetDir, "sifty.db")
	settingsPath := filepath.Join(targetDir, "sifty.yaml")

	var buf bytes.Buffer
	err := importv1.Import(context.Background(), dir, dbPath, settingsPath, &buf)
	if err == nil {
		t.Fatal("expected error for wrong version, got nil")
	}
	if !strings.Contains(err.Error(), "42") {
		t.Errorf("error: %v", err)
	}
	// Target folder must be cleaned up.
	if _, statErr := os.Stat(targetDir); !os.IsNotExist(statErr) {
		t.Errorf("target folder should not exist after failed import")
	}
}

func TestImport_NonEmptyTargetRefused(t *testing.T) {
	fromDir := makeOldDir(t, "")

	// Create a non-empty target directory.
	targetDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(targetDir, "existing.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(targetDir, "sifty.db")
	settingsPath := filepath.Join(targetDir, "sifty.yaml")

	var buf bytes.Buffer
	err := importv1.Import(context.Background(), fromDir, dbPath, settingsPath, &buf)
	if err == nil {
		t.Fatal("expected error for non-empty target, got nil")
	}
	if !strings.Contains(err.Error(), "not empty") {
		t.Errorf("error: %v", err)
	}
}

func TestImport_OldFileUnchangedAfterImport(t *testing.T) {
	fromDir := makeOldDir(t, "")
	oldDB := openOldDB(t, fromDir)

	_, err := oldDB.Exec(`
		INSERT INTO jobs (id, identity, title, company, original_url,
		  description, description_status, first_seen, last_seen)
		VALUES (1,'co|t','T','Co','https://example.com/jobs/1','t','available','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	oldDB.Close()

	// Read bytes of old database before import.
	oldPath := filepath.Join(fromDir, oldDBFile)
	beforeBytes, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	targetDir := t.TempDir()
	runImport(t, fromDir, targetDir)

	afterBytes, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(beforeBytes, afterBytes) {
		t.Errorf("old database changed after import")
	}
}

func TestImport_ReportContainsCountsAndIDsOnly(t *testing.T) {
	fromDir := makeOldDir(t, "")
	oldDB := openOldDB(t, fromDir)

	const vacURL = "https://example.com/jobs/secret-title-company"
	_, err := oldDB.Exec(`
		INSERT INTO jobs (id, identity, title, company, original_url,
		  description, description_status, first_seen, last_seen)
		VALUES (1,'secret|title','Secret Title','Secret Company',?,
		  'secret description','available','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`,
		vacURL)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	targetDir := t.TempDir()
	report := runImport(t, fromDir, targetDir)

	// No URL, company, title or description text should appear.
	if strings.Contains(report, "secret") {
		t.Errorf("report contains text that should not be there: %s", report)
	}
}

func TestImport_DateWithoutZoneReadAsUTC(t *testing.T) {
	fromDir := makeOldDir(t, "")
	oldDB := openOldDB(t, fromDir)

	// Date without timezone.
	_, err := oldDB.Exec(`
		INSERT INTO jobs (id, identity, title, company, original_url,
		  description, description_status, published_at, published_basis, first_seen, last_seen)
		VALUES (1,'co|t','T','Co','https://example.com/jobs/1',
		  't','available','2024-05-10T14:30:00',NULL,'2024-05-10T14:30:00','2024-05-10T14:30:00')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	targetDir := t.TempDir()
	runImport(t, fromDir, targetDir)
	newDB := openNewDB(t, targetDir)

	var firstSeen, publishedAt string
	newDB.QueryRow(`SELECT first_seen, published_at FROM vacancy WHERE id=1`).Scan(&firstSeen, &publishedAt) //nolint:errcheck

	// Without zone, treated as UTC, should be normalised.
	if firstSeen != "2024-05-10T14:30:00Z" {
		t.Errorf("first_seen: got %q, want 2024-05-10T14:30:00Z", firstSeen)
	}
	if publishedAt != "2024-05-10T14:30:00Z" {
		t.Errorf("published_at: got %q", publishedAt)
	}
}

func TestImport_VacancyDroppedWhenCanonicalFails(t *testing.T) {
	fromDir := makeOldDir(t, "")
	oldDB := openOldDB(t, fromDir)

	// Use a URL with an unsupported scheme.
	_, err := oldDB.Exec(`
		INSERT INTO jobs (id, identity, title, company, original_url,
		  description, description_status, first_seen, last_seen)
		VALUES (1,'co|t','T','Co','ftp://example.com/jobs/1',
		  't','available','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	targetDir := t.TempDir()
	report := runImport(t, fromDir, targetDir)
	newDB := openNewDB(t, targetDir)

	var n int
	newDB.QueryRow(`SELECT count(*) FROM vacancy`).Scan(&n) //nolint:errcheck
	if n != 0 {
		t.Errorf("vacancy count: got %d, want 0", n)
	}
	if !strings.Contains(report, "dropped id=1") {
		t.Errorf("report missing drop notice: %s", report)
	}
}
