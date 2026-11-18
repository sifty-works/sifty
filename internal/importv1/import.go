// Package importv1 reads a copy of the old application database and writes
// into a new, empty data folder. It is one-time code, not counted in budgets.
package importv1

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sifty-works/sifty/internal/catalog"
	"github.com/sifty-works/sifty/internal/config"
	"github.com/sifty-works/sifty/internal/db"
	"github.com/sifty-works/sifty/internal/listing"
	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite" // driver
)

const (
	oldDBFile     = "store.sqlite"
	oldConfigFile = "config/search.yaml"
	oldVersion    = 43
	tempDBName    = ".import-tmp.db"
)

// --- report types ---

// dropReason records one dropped item.
type dropReason struct {
	ID     int64
	Reason string
}

// dupPair records a duplicate canonical address: KeptID keeps the vacancy,
// AliasedID becomes an alias.
type dupPair struct {
	KeptID    int64
	AliasedID int64
}

// importReport accumulates statistics for the final report.
type importReport struct {
	VacanciesRead    int
	VacanciesWritten int
	VacanciesDropped []dropReason
	Duplicates       []dupPair

	SightingsRead    int
	SightingsWritten int
	SightingsSkipped int // duplicate canonical address

	AliasesRead    int
	AliasesWritten int
	AliasesDropped []dropReason

	TombstonesRead    int
	TombstonesWritten int

	SourcesRead    int
	SourcesWritten int
	SourcesDropped []struct {
		Name   string
		Reason string
	}

	LastRunFound   bool
	LastRunWritten bool

	ExclusionsWritten  int
	ProfileExceptWords []string // words from per-profile except lists, reported but not imported

	NonImportedKeys []string
}

// --- old schema row types ---

type oldJob struct {
	id                int64
	identity          string
	title             string
	company           string
	originalURL       string
	applyURL          sql.NullString
	description       string
	descriptionStatus string
	location          sql.NullString
	publishedAt       sql.NullString
	publishedBasis    sql.NullString
	expiresAt         sql.NullString
	firstSeen         string
	lastSeen          string
	tags              string
	descriptionHTML   sql.NullString
}

type oldSighting struct {
	jobID  int64
	source string
	url    string
	seen   string
}

type oldAlias struct {
	oldID int64
	newID int64
}

type oldConfig struct {
	Sites []struct {
		Name    string `yaml:"name"`
		URL     string `yaml:"url"`
		Enabled bool   `yaml:"enabled"`
	} `yaml:"sites"`
	// remaining top-level keys are captured to report as not imported
}

// Import reads the old database from fromDir and writes into the new database
// at dbPath with settings at settingsPath. The data directory is the parent of
// dbPath. Progress and the final report are written to out.
func Import(ctx context.Context, fromDir, dbPath, settingsPath string, out io.Writer) error {
	dataDir := filepath.Dir(dbPath)

	// Refuse a non-empty target.
	if info, err := os.Stat(dataDir); err == nil && info.IsDir() {
		entries, err := os.ReadDir(dataDir)
		if err != nil {
			return fmt.Errorf("reading target folder: %w", err)
		}
		if len(entries) > 0 {
			return fmt.Errorf("The target folder is not empty.")
		}
	}

	// Create target folder.
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return fmt.Errorf("creating target folder: %w", err)
	}

	var importErr error
	defer func() {
		if importErr != nil {
			os.RemoveAll(dataDir)
		}
	}()

	importErr = runImport(ctx, fromDir, dbPath, settingsPath, dataDir, out)
	return importErr
}

func runImport(ctx context.Context, fromDir, dbPath, settingsPath, dataDir string, out io.Writer) error {
	// Open old DB read-only.
	oldDBPath := filepath.Join(fromDir, oldDBFile)
	oldDSN := "file:" + oldDBPath + "?mode=ro"
	oldDB, err := sql.Open("sqlite", oldDSN)
	if err != nil {
		return fmt.Errorf("opening old database: %w", err)
	}
	defer oldDB.Close()

	// VACUUM INTO temp file in target folder.
	tempPath := filepath.Join(dataDir, tempDBName)
	if _, err := oldDB.ExecContext(ctx, "VACUUM INTO ?", tempPath); err != nil {
		return fmt.Errorf("copying old database: %w", err)
	}
	oldDB.Close()
	defer os.Remove(tempPath)

	// Open copy and check version.
	copyDB, err := sql.Open("sqlite", tempPath)
	if err != nil {
		return fmt.Errorf("opening database copy: %w", err)
	}
	defer copyDB.Close()

	var version int
	if err := copyDB.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("reading database version: %w", err)
	}
	if version != oldVersion {
		return fmt.Errorf("Unknown database version %d; this import reads version 43.", version)
	}

	// Open new database.
	newDB, err := db.Open(dbPath, db.Migrations)
	if err != nil {
		return fmt.Errorf("opening new database: %w", err)
	}
	defer newDB.Close()

	// Open settings store.
	store, err := config.Open(settingsPath)
	if err != nil {
		return fmt.Errorf("opening settings: %w", err)
	}

	// Load old search.yaml.
	oldCfg, nonImportedKeys, err := loadOldConfig(filepath.Join(fromDir, oldConfigFile))
	if err != nil {
		// Missing config is not fatal.
		oldCfg = &oldConfig{}
		nonImportedKeys = nil
	}

	// Run the import.
	report, err := doImport(ctx, copyDB, newDB, store, oldCfg)
	if err != nil {
		return err
	}
	report.NonImportedKeys = nonImportedKeys

	// Write report.
	reportText := formatReport(report)
	fmt.Fprint(out, reportText)
	reportPath := filepath.Join(dataDir, "import-report.txt")
	if err := os.WriteFile(reportPath, []byte(reportText), 0600); err != nil {
		fmt.Fprintf(out, "warning: could not write report file: %v\n", err)
	}

	return nil
}

// doImport runs the full import inside transactions and returns the report.
func doImport(ctx context.Context, copyDB *sql.DB, newDB *db.DB, store *config.Store, oldCfg *oldConfig) (*importReport, error) {
	report := &importReport{}

	// --- Read old data ---

	jobs, err := readJobs(ctx, copyDB)
	if err != nil {
		return nil, fmt.Errorf("reading jobs: %w", err)
	}

	sightings, err := readSightings(ctx, copyDB)
	if err != nil {
		return nil, fmt.Errorf("reading sightings: %w", err)
	}

	aliases, err := readAliases(ctx, copyDB)
	if err != nil {
		return nil, fmt.Errorf("reading aliases: %w", err)
	}

	hashes, err := readDeletedHashes(ctx, copyDB)
	if err != nil {
		return nil, fmt.Errorf("reading deleted_listings: %w", err)
	}

	idSeq, err := readIDSequence(ctx, copyDB)
	if err != nil {
		return nil, fmt.Errorf("reading id_sequence: %w", err)
	}

	lastRunTime, err := readLastRun(ctx, copyDB)
	if err != nil {
		return nil, fmt.Errorf("reading collection_runs: %w", err)
	}
	report.LastRunFound = !lastRunTime.IsZero()

	// Build source name → key map from old config.
	sourceKeyByName := buildSourceKeyMap(oldCfg)

	// Build set of source names seen in sightings.
	seenSourceNames := make(map[string]bool)
	for _, s := range sightings {
		seenSourceNames[s.source] = true
	}

	// --- Process vacancies ---

	report.VacanciesRead = len(jobs)

	// canonMap maps canonical URL → job id (for duplicate detection).
	canonMap := make(map[string]int64)
	// validJobs is the slice of (job, canonURL) for jobs that parse successfully.
	type validJob struct {
		job      *oldJob
		canonURL string
	}
	var validJobs []validJob

	for i := range jobs {
		j := &jobs[i]
		canon, err := listing.Canonical(j.originalURL)
		if err != nil {
			report.VacanciesDropped = append(report.VacanciesDropped, dropReason{ID: j.id, Reason: "no canonical address"})
			continue
		}
		validJobs = append(validJobs, validJob{job: j, canonURL: canon})
	}

	// Sort by id ascending so lower ids are processed first (kept in duplicates).
	sort.Slice(validJobs, func(i, k int) bool {
		return validJobs[i].job.id < validJobs[k].job.id
	})

	// Detect duplicates: map canon → first seen id.
	// duplicateAlias maps higher-id job → lower-id job (kept).
	duplicateAlias := make(map[int64]int64) // aliasedID → keptID
	for _, vj := range validJobs {
		if keptID, found := canonMap[vj.canonURL]; found {
			duplicateAlias[vj.job.id] = keptID
			report.Duplicates = append(report.Duplicates, dupPair{KeptID: keptID, AliasedID: vj.job.id})
		} else {
			canonMap[vj.canonURL] = vj.job.id
		}
	}

	// Build a map of aliasedID → keptID for use in sighting remapping.
	// Also compute merge data for kept vacancies.
	type mergeData struct {
		firstSeen      string
		publishedAt    string
		publishedBasis string
		lastSeen       string
		textState      listing.TextState
		body           []byte
		hash           string
	}
	mergedData := make(map[int64]*mergeData) // keptID → best values

	// First pass: initialise merge data from kept vacancies.
	for _, vj := range validJobs {
		if _, isAlias := duplicateAlias[vj.job.id]; isAlias {
			continue
		}
		// first_seen and last_seen parse failures are fatal.
		firstSeen := normDateUTC(vj.job.firstSeen)
		if firstSeen == "" {
			return nil, fmt.Errorf("vacancy %d: first_seen cannot be parsed: %q", vj.job.id, vj.job.firstSeen)
		}
		lastSeen := normDateUTC(vj.job.lastSeen)
		if lastSeen == "" {
			return nil, fmt.Errorf("vacancy %d: last_seen cannot be parsed: %q", vj.job.id, vj.job.lastSeen)
		}
		ts, body, hash, err := encodeVacancy(vj.job)
		if err != nil {
			report.VacanciesDropped = append(report.VacanciesDropped, dropReason{ID: vj.job.id, Reason: "encode: " + err.Error()})
			delete(canonMap, vj.canonURL)
			continue
		}
		pub, pubBasis := parsePublishedAt(vj.job.publishedAt, vj.job.publishedBasis)
		mergedData[vj.job.id] = &mergeData{
			firstSeen:      firstSeen,
			publishedAt:    pub,
			publishedBasis: pubBasis,
			lastSeen:       lastSeen,
			textState:      ts,
			body:           body,
			hash:           hash,
		}
	}

	// Second pass: merge aliased vacancies into kept.
	for _, vj := range validJobs {
		keptID, isAlias := duplicateAlias[vj.job.id]
		if !isAlias {
			continue
		}
		kept, ok := mergedData[keptID]
		if !ok {
			continue
		}
		ts, body, hash, err := encodeVacancy(vj.job)
		if err != nil {
			continue
		}
		pub, pubBasis := parsePublishedAt(vj.job.publishedAt, vj.job.publishedBasis)
		// Earliest first_seen.
		fs := normDateUTC(vj.job.firstSeen)
		if kept.firstSeen == "" || (fs != "" && fs < kept.firstSeen) {
			kept.firstSeen = fs
		}
		// Earliest publishedAt.
		if pub != "" && (kept.publishedAt == "" || pub < kept.publishedAt) {
			kept.publishedAt = pub
			kept.publishedBasis = pubBasis
		}
		// Latest lastSeen.
		ls := normDateUTC(vj.job.lastSeen)
		if ls > kept.lastSeen {
			kept.lastSeen = ls
		}
		// Better text: full > summary > none.
		if betterTextState(ts, kept.textState) {
			kept.textState = ts
			kept.body = body
			kept.hash = hash
		}
	}

	// Build keepIDs: set of job ids that become actual vacancies.
	keepIDs := make(map[int64]bool)
	for _, vj := range validJobs {
		if _, isAlias := duplicateAlias[vj.job.id]; !isAlias {
			if _, ok := mergedData[vj.job.id]; ok {
				keepIDs[vj.job.id] = true
			}
		}
	}

	// --- Write all in one transaction ---

	tx, err := newDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Write vacancies.
	for _, vj := range validJobs {
		j := vj.job
		if !keepIDs[j.id] {
			continue
		}
		md := mergedData[j.id]
		company := j.company
		if strings.EqualFold(company, "Unknown") {
			company = "" // the old placeholder for a missing company
		}
		identity := listing.Identity(company, j.title)

		// Apply URL.
		applyURL := ""
		if j.applyURL.Valid && j.applyURL.String != "" {
			if ca, ok := listing.ConcreteATS(j.applyURL.String); ok {
				applyURL = ca
			}
		}

		location := ""
		if j.location.Valid {
			location = j.location.String
		}

		// Expire date: drop if doesn't parse.
		expiresAt := ""
		if j.expiresAt.Valid && j.expiresAt.String != "" {
			if t, err := parseOldDate(j.expiresAt.String); err == nil {
				expiresAt = t.UTC().Format(time.RFC3339)
			}
		}

		_, err := tx.ExecContext(ctx, `
			INSERT INTO vacancy
				(id, identity, title, company, url, apply_url, location, tags, source_fields,
				 published_at, published_basis, expires_at,
				 first_seen, last_seen, text_state, content_hash, version, body)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			j.id, identity, j.title, company, vj.canonURL, applyURL, location,
			j.tags, "{}",
			md.publishedAt, md.publishedBasis, expiresAt,
			md.firstSeen, md.lastSeen, string(md.textState), md.hash, 1, md.body,
		)
		if err != nil {
			return nil, fmt.Errorf("inserting vacancy %d: %w", j.id, err)
		}
		if len(md.body) > 0 {
			if err := catalog.WriteFTS(ctx, tx, j.id, md.body); err != nil {
				return nil, fmt.Errorf("fts for vacancy %d: %w", j.id, err)
			}
		}
		report.VacanciesWritten++
	}

	// Write sightings.
	// Build sightings by jobID.
	sightingsByJob := make(map[int64][]oldSighting)
	for _, s := range sightings {
		sightingsByJob[s.jobID] = append(sightingsByJob[s.jobID], s)
	}

	report.SightingsRead = len(sightings)

	// Track canonical URLs already used in vacancy_source.
	usedCanonURLs := make(map[string]bool)

	for _, vj := range validJobs {
		j := vj.job
		if !keepIDs[j.id] {
			continue
		}
		jobSightings := sightingsByJob[j.id]
		// Also gather sightings for aliased duplicates.
		for _, dp := range report.Duplicates {
			if dp.KeptID == j.id {
				jobSightings = append(jobSightings, sightingsByJob[dp.AliasedID]...)
			}
		}

		// Find earliest sighting source for own-address row.
		ownSource := "name:import"
		ownSeenAt := normDateUTC(j.lastSeen)
		if len(jobSightings) > 0 {
			// Sort by seen_at to find earliest.
			sort.Slice(jobSightings, func(a, b int) bool {
				return jobSightings[a].seen < jobSightings[b].seen
			})
			earliestSighting := jobSightings[0]
			ownSource = resolveSource(earliestSighting.source, sourceKeyByName)
		}

		// Insert own-address row first.
		if !usedCanonURLs[vj.canonURL] {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO vacancy_source (vacancy_id, source, url, seen_at) VALUES (?,?,?,?)`,
				j.id, ownSource, vj.canonURL, ownSeenAt,
			); err != nil {
				return nil, fmt.Errorf("inserting own-address sighting for %d: %w", j.id, err)
			}
			usedCanonURLs[vj.canonURL] = true
			report.SightingsWritten++
		}

		// Insert per-sighting rows.
		for _, s := range jobSightings {
			canonSightURL, err := listing.Canonical(s.url)
			if err != nil {
				report.SightingsSkipped++
				continue
			}
			if usedCanonURLs[canonSightURL] {
				report.SightingsSkipped++
				continue
			}
			srcKey := resolveSource(s.source, sourceKeyByName)
			seenAt := normDateUTC(s.seen)
			if seenAt == "" {
				seenAt = ownSeenAt
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO vacancy_source (vacancy_id, source, url, seen_at) VALUES (?,?,?,?)`,
				j.id, srcKey, canonSightURL, seenAt,
			); err != nil {
				return nil, fmt.Errorf("inserting sighting for %d: %w", j.id, err)
			}
			usedCanonURLs[canonSightURL] = true
			report.SightingsWritten++
		}
	}

	// Write aliases from duplicate pairs.
	for _, dp := range report.Duplicates {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO vacancy_alias (old_id, new_id) VALUES (?,?)`,
			dp.AliasedID, dp.KeptID,
		); err != nil {
			return nil, fmt.Errorf("inserting duplicate alias %d→%d: %w", dp.AliasedID, dp.KeptID, err)
		}
	}

	// Write job_aliases (from old table), following chains to imported vacancies.
	report.AliasesRead = len(aliases)
	// Build a chain-resolution map.
	// First, build direct map from old_aliases.
	aliasMap := make(map[int64]int64, len(aliases)) // old_id → new_id
	for _, a := range aliases {
		aliasMap[a.oldID] = a.newID
	}

	for _, a := range aliases {
		// Follow chain.
		target, reason := resolveAliasTarget(a.oldID, a.newID, aliasMap, keepIDs, 50)
		if reason != "" {
			report.AliasesDropped = append(report.AliasesDropped, dropReason{ID: a.oldID, Reason: reason})
			continue
		}
		// old_id must not itself be an imported vacancy.
		if keepIDs[a.oldID] {
			report.AliasesDropped = append(report.AliasesDropped, dropReason{ID: a.oldID, Reason: "old_id is an imported vacancy"})
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO vacancy_alias (old_id, new_id) VALUES (?,?)`,
			a.oldID, target,
		); err != nil {
			return nil, fmt.Errorf("inserting alias %d→%d: %w", a.oldID, target, err)
		}
		report.AliasesWritten++
	}

	// Write tombstones.
	report.TombstonesRead = len(hashes)
	for _, h := range hashes {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO tombstone (key) VALUES (?)`,
			"legacy:"+h,
		); err != nil {
			return nil, fmt.Errorf("inserting tombstone: %w", err)
		}
		report.TombstonesWritten++
	}

	// Set next id: max of all vacancy ids, alias ids, and id_sequence value.
	var maxVacID, maxAliasID int64
	tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM vacancy`).Scan(&maxVacID)             //nolint:errcheck
	tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(old_id),0) FROM vacancy_alias`).Scan(&maxAliasID) //nolint:errcheck
	maxID := maxVacID
	if maxAliasID > maxID {
		maxID = maxAliasID
	}
	if idSeq > maxID {
		maxID = idSeq
	}

	// Set sqlite_sequence for vacancy so AUTOINCREMENT never reuses old ids.
	if maxID > 0 {
		res, err := tx.ExecContext(ctx,
			`UPDATE sqlite_sequence SET seq=? WHERE name='vacancy' AND seq<?`,
			maxID, maxID,
		)
		if err != nil {
			return nil, fmt.Errorf("updating sqlite_sequence: %w", err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			// Row absent (no vacancies were inserted) — INSERT it directly.
			var exists int
			tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_sequence WHERE name='vacancy'`).Scan(&exists) //nolint:errcheck
			if exists == 0 {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO sqlite_sequence (name, seq) VALUES ('vacancy', ?)`, maxID,
				); err != nil {
					return nil, fmt.Errorf("inserting sqlite_sequence: %w", err)
				}
			}
			// If exists > 0 but rows == 0, the current seq is already >= maxID; leave it.
		}
	}

	// Write exclusions from stop_words (word kind) and block_rules company kind.
	stopWords, err := readStopWords(ctx, copyDB)
	if err != nil {
		return nil, fmt.Errorf("reading stop_words: %w", err)
	}
	for _, sw := range stopWords {
		value := strings.TrimSpace(sw.word)
		if value == "" {
			continue
		}
		norm := strings.ToLower(value)
		res, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO exclusion (kind,value,norm,profile_id,created) VALUES ('word',?,?,0,?)`,
			value, norm, sw.created)
		if err != nil {
			return nil, fmt.Errorf("inserting word exclusion %q: %w", value, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			report.ExclusionsWritten++
		}
	}
	companies, err := readBlockRules(ctx, copyDB)
	if err != nil {
		return nil, fmt.Errorf("reading block_rules: %w", err)
	}
	for _, c := range companies {
		value := strings.TrimSpace(c.value)
		if value == "" || strings.EqualFold(value, "Unknown") {
			continue
		}
		norm := strings.TrimSuffix(listing.Identity(value, ""), "|")
		res, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO exclusion (kind,value,norm,profile_id,created) VALUES ('company',?,?,0,?)`,
			value, norm, c.created)
		if err != nil {
			return nil, fmt.Errorf("inserting company exclusion %q: %w", value, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			report.ExclusionsWritten++
		}
	}
	profileWords, err := readProfileExceptWords(ctx, copyDB)
	if err != nil {
		return nil, fmt.Errorf("reading profile except words: %w", err)
	}
	report.ProfileExceptWords = profileWords

	// Write last run as a job row.
	if !lastRunTime.IsZero() {
		ts := lastRunTime.UTC().Format("2006-01-02T15:04:05Z")
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO job (kind, subject, priority, state, attempts, not_before, progress, error, params, created, updated)
			VALUES ('collect', '', 0, 'done', 1, ?, '{}', '', NULL, ?, ?)`,
			ts, ts, ts,
		); err != nil {
			return nil, fmt.Errorf("inserting last run: %w", err)
		}
		report.LastRunWritten = true
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing transaction: %w", err)
	}

	// --- Write sources via config.Store.Update ---

	sourceSightings := buildSourceSightingSet(sightings)
	report.SourcesRead = len(oldCfg.Sites)

	seenSourceKeys := make(map[string]bool)
	var newSources []config.SourceConfig

	for _, site := range oldCfg.Sites {
		key, err := config.SourceKey(site.URL)
		if err != nil {
			report.SourcesDropped = append(report.SourcesDropped, struct {
				Name   string
				Reason string
			}{Name: site.Name, Reason: "invalid address: " + err.Error()})
			continue
		}
		if len([]rune(site.Name)) > 100 {
			report.SourcesDropped = append(report.SourcesDropped, struct {
				Name   string
				Reason string
			}{Name: site.Name, Reason: "name longer than 100 characters"})
			continue
		}
		if seenSourceKeys[key] {
			report.SourcesDropped = append(report.SourcesDropped, struct {
				Name   string
				Reason string
			}{Name: site.Name, Reason: "duplicate source key"})
			continue
		}
		if !sourceSightings[site.Name] {
			report.SourcesDropped = append(report.SourcesDropped, struct {
				Name   string
				Reason string
			}{Name: site.Name, Reason: "no vacancy"})
			continue
		}
		seenSourceKeys[key] = true
		newSources = append(newSources, config.SourceConfig{
			Name:    site.Name,
			URL:     site.URL,
			Enabled: site.Enabled,
		})
	}

	if err := store.Update(func(c *config.Config) error {
		c.Sources = newSources
		return nil
	}); err != nil {
		return nil, fmt.Errorf("writing sources: %w", err)
	}
	report.SourcesWritten = len(newSources)

	return report, nil
}

// --- helpers ---

// resolveAliasTarget follows the alias chain starting from (oldID → nextID)
// up to maxDepth steps, returning the final target vacancy id or a reason
// why the alias should be dropped.
func resolveAliasTarget(oldID, nextID int64, aliasMap map[int64]int64, keepIDs map[int64]bool, maxDepth int) (int64, string) {
	visited := map[int64]bool{oldID: true}
	cur := nextID
	for i := 0; i < maxDepth; i++ {
		if visited[cur] {
			return 0, "loop in alias chain"
		}
		if keepIDs[cur] {
			return cur, ""
		}
		next, ok := aliasMap[cur]
		if !ok {
			return 0, "target not an imported vacancy"
		}
		visited[cur] = true
		cur = next
	}
	return 0, "alias chain too long"
}

// resolveSource maps an old sighting source name to the new source key.
func resolveSource(name string, sourceKeyByName map[string]string) string {
	if key, ok := sourceKeyByName[name]; ok {
		return key
	}
	return "name:" + name
}

// buildSourceKeyMap builds a name→key map from the old config.
func buildSourceKeyMap(cfg *oldConfig) map[string]string {
	m := make(map[string]string, len(cfg.Sites))
	for _, site := range cfg.Sites {
		key, err := config.SourceKey(site.URL)
		if err != nil {
			continue
		}
		m[site.Name] = key
	}
	return m
}

// buildSourceSightingSet returns a set of source names that appear in sightings.
func buildSourceSightingSet(sightings []oldSighting) map[string]bool {
	m := make(map[string]bool)
	for _, s := range sightings {
		m[s.source] = true
	}
	return m
}

// encodeVacancy computes text state, body and hash for an old job row.
func encodeVacancy(j *oldJob) (listing.TextState, []byte, string, error) {
	// Determine text state.
	var ts listing.TextState
	if strings.HasPrefix(j.descriptionStatus, "blocked") {
		ts = listing.TextBlocked
	} else if strings.HasPrefix(j.descriptionStatus, "available") {
		ts = listing.TextFull
	} else {
		// Check if there's any text.
		hasText := (j.descriptionHTML.Valid && j.descriptionHTML.String != "") ||
			j.description != ""
		if !hasText {
			ts = listing.TextNotFetched
		} else {
			ts = listing.TextSummary
		}
	}

	// Pick HTML or plain text.
	var rawText string
	var kind listing.Kind
	if j.descriptionHTML.Valid && j.descriptionHTML.String != "" {
		rawText = j.descriptionHTML.String
		kind = listing.HTML
	} else {
		rawText = j.description
		kind = listing.Plain
	}

	if rawText == "" {
		return ts, []byte{}, "", nil
	}

	normalized := listing.Normalize(rawText, kind)
	body, hash, err := catalog.Encode(normalized)
	if err != nil {
		return ts, nil, "", err
	}
	return ts, body, hash, nil
}

// betterTextState reports whether candidate is strictly better than current.
func betterTextState(candidate, current listing.TextState) bool {
	return textRank(candidate) > textRank(current)
}

func textRank(s listing.TextState) int {
	switch s {
	case listing.TextFull:
		return 2
	case listing.TextSummary:
		return 1
	}
	return 0
}

// parsePublishedAt returns the published_at and published_basis values to store.
func parsePublishedAt(pubAt, pubBasis sql.NullString) (string, string) {
	if !pubAt.Valid || pubAt.String == "" {
		return "", ""
	}
	t, err := parseOldDate(pubAt.String)
	if err != nil {
		return "", "" // drop unparseable date
	}
	// Drop future dates.
	if t.After(time.Now().UTC()) {
		return "", ""
	}
	pub := t.UTC().Format(time.RFC3339)
	basis := ""
	if pubBasis.Valid {
		basis = pubBasis.String
	}
	// Keep basis only for datePosted or pubDate; otherwise store without basis.
	if basis != "datePosted" && basis != "pubDate" {
		basis = ""
	}
	return pub, basis
}

// normDateUTC parses an old date and returns UTC RFC 3339, or "" on error.
func normDateUTC(s string) string {
	if s == "" {
		return ""
	}
	t, err := parseOldDate(s)
	if err != nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// parseOldDate tries to parse an ISO 8601 date in several formats.
// A date without a timezone is treated as UTC.
func parseOldDate(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05.000",
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse date %q", s)
}

// --- DB readers ---

type oldStopWord struct{ word, created string }
type oldBlockRule struct{ value, created string }

func readStopWords(ctx context.Context, db *sql.DB) ([]oldStopWord, error) {
	rows, err := db.QueryContext(ctx, `SELECT word, created_at FROM stop_words ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []oldStopWord
	for rows.Next() {
		var r oldStopWord
		if err := rows.Scan(&r.word, &r.created); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func readBlockRules(ctx context.Context, db *sql.DB) ([]oldBlockRule, error) {
	rows, err := db.QueryContext(ctx, `SELECT value, created_at FROM block_rules WHERE kind='company' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []oldBlockRule
	for rows.Next() {
		var r oldBlockRule
		if err := rows.Scan(&r.value, &r.created); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// readProfileExceptWords collects the unique except-words from all user profile data blobs.
func readProfileExceptWords(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT data FROM user_profiles`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := make(map[string]string) // lowercase → original
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			continue
		}
		var d struct {
			Search struct {
				Except []string `json:"except"`
			} `json:"search"`
		}
		if err := json.Unmarshal([]byte(data), &d); err != nil {
			continue
		}
		for _, w := range d.Search.Except {
			if w = strings.TrimSpace(w); w != "" {
				lower := strings.ToLower(w)
				if _, ok := seen[lower]; !ok {
					seen[lower] = w
				}
			}
		}
	}
	var out []string
	for _, v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out, rows.Err()
}

func readJobs(ctx context.Context, db *sql.DB) ([]oldJob, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, identity, title, company, original_url, apply_url,
		       description, description_status, location,
		       published_at, published_basis, expires_at,
		       first_seen, last_seen, tags, description_html
		FROM jobs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []oldJob
	for rows.Next() {
		var j oldJob
		if err := rows.Scan(
			&j.id, &j.identity, &j.title, &j.company, &j.originalURL, &j.applyURL,
			&j.description, &j.descriptionStatus, &j.location,
			&j.publishedAt, &j.publishedBasis, &j.expiresAt,
			&j.firstSeen, &j.lastSeen, &j.tags, &j.descriptionHTML,
		); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func readSightings(ctx context.Context, db *sql.DB) ([]oldSighting, error) {
	rows, err := db.QueryContext(ctx, `SELECT job_id, source, url, seen FROM sightings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sightings []oldSighting
	for rows.Next() {
		var s oldSighting
		if err := rows.Scan(&s.jobID, &s.source, &s.url, &s.seen); err != nil {
			return nil, err
		}
		sightings = append(sightings, s)
	}
	return sightings, rows.Err()
}

func readAliases(ctx context.Context, db *sql.DB) ([]oldAlias, error) {
	rows, err := db.QueryContext(ctx, `SELECT old_id, new_id FROM job_aliases`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var aliases []oldAlias
	for rows.Next() {
		var a oldAlias
		if err := rows.Scan(&a.oldID, &a.newID); err != nil {
			return nil, err
		}
		aliases = append(aliases, a)
	}
	return aliases, rows.Err()
}

func readDeletedHashes(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT hash FROM deleted_listings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hashes []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		hashes = append(hashes, h)
	}
	return hashes, rows.Err()
}

func readIDSequence(ctx context.Context, db *sql.DB) (int64, error) {
	var v int64
	err := db.QueryRowContext(ctx, `SELECT value FROM id_sequence WHERE name='jobs'`).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return v - 1, nil // id_sequence.value is the next id; highest used = value-1
}

func readLastRun(ctx context.Context, db *sql.DB) (time.Time, error) {
	var s sql.NullString
	err := db.QueryRowContext(ctx, `SELECT MAX(started) FROM collection_runs`).Scan(&s)
	if err != nil || !s.Valid || s.String == "" {
		return time.Time{}, nil
	}
	t, parseErr := parseOldDate(s.String)
	if parseErr != nil {
		return time.Time{}, nil
	}
	return t, nil
}

// --- old config loading ---

// loadOldConfig parses search.yaml and returns the config and the list of keys
// that are present but not imported.
func loadOldConfig(path string) (*oldConfig, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &oldConfig{}, nil, nil
		}
		return nil, nil, err
	}

	var raw map[string]yaml.Node
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("parsing search.yaml: %w", err)
	}

	var cfg oldConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, nil, fmt.Errorf("parsing search.yaml sites: %w", err)
	}

	// Collect non-imported keys.
	var nonImported []string
	for k := range raw {
		if k != "version" && k != "sites" {
			nonImported = append(nonImported, k)
		}
	}
	sort.Strings(nonImported)

	return &cfg, nonImported, nil
}

// --- report formatting ---

func formatReport(r *importReport) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "Vacancies: %d read, %d written, %d dropped\n",
		r.VacanciesRead, r.VacanciesWritten, len(r.VacanciesDropped))
	for _, d := range r.VacanciesDropped {
		fmt.Fprintf(&sb, "  dropped id=%d: %s\n", d.ID, d.Reason)
	}
	if len(r.Duplicates) > 0 {
		fmt.Fprintf(&sb, "  duplicate canonical address pairs (%d):\n", len(r.Duplicates))
		for _, dp := range r.Duplicates {
			fmt.Fprintf(&sb, "    kept=%d aliased=%d\n", dp.KeptID, dp.AliasedID)
		}
	}

	fmt.Fprintf(&sb, "Sightings: %d read, %d written, %d skipped\n",
		r.SightingsRead, r.SightingsWritten, r.SightingsSkipped)

	fmt.Fprintf(&sb, "Aliases: %d read, %d written, %d dropped\n",
		r.AliasesRead, r.AliasesWritten, len(r.AliasesDropped))
	for _, d := range r.AliasesDropped {
		fmt.Fprintf(&sb, "  dropped alias id=%d: %s\n", d.ID, d.Reason)
	}

	fmt.Fprintf(&sb, "Tombstones: %d read, %d written\n",
		r.TombstonesRead, r.TombstonesWritten)

	fmt.Fprintf(&sb, "Sources: %d read, %d written, %d dropped\n",
		r.SourcesRead, r.SourcesWritten, len(r.SourcesDropped))
	for _, d := range r.SourcesDropped {
		fmt.Fprintf(&sb, "  dropped %q: %s\n", d.Name, d.Reason)
	}

	if r.LastRunFound {
		if r.LastRunWritten {
			fmt.Fprintf(&sb, "Last run: written\n")
		} else {
			fmt.Fprintf(&sb, "Last run: found but not written\n")
		}
	} else {
		fmt.Fprintf(&sb, "Last run: none found\n")
	}

	fmt.Fprintf(&sb, "Exclusions: %d written\n", r.ExclusionsWritten)
	if len(r.ProfileExceptWords) > 0 {
		fmt.Fprintf(&sb, "Per-profile except words not imported: %s\n",
			strings.Join(r.ProfileExceptWords, ", "))
	}

	if len(r.NonImportedKeys) > 0 {
		fmt.Fprintf(&sb, "Keys in search.yaml not imported: %s\n",
			strings.Join(r.NonImportedKeys, ", "))
	}

	return sb.String()
}
