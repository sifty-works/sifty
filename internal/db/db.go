// Package db opens the SQLite database, runs migrations and makes backups.
// No other package reaches the raw connection.
package db

import (
	"database/sql"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // register the "sqlite" driver
)

// embedded holds the raw embed.FS for the migrations folder.
//
//go:embed all:migrations
var embedded embed.FS

// Migrations is the embedded migration folder shipped with the binary.
// Files are served at the top level (e.g. "001_catalog.sql"), not under a
// "migrations/" prefix, so callers pass db.Migrations directly to db.Open.
var Migrations fs.FS

func init() {
	sub, err := fs.Sub(embedded, "migrations")
	if err != nil {
		panic("db: cannot sub migrations FS: " + err.Error())
	}
	Migrations = sub
}

// DB wraps a *sql.DB with the applied migration version.
type DB struct {
	*sql.DB
	Version int
}

// Open opens or creates the SQLite database at path, runs pending migrations
// from the given fs.FS (which must contain files matching NNN_<name>.sql),
// and returns the opened DB.
//
// Before running pending migrations on an existing (non-new) database, it
// backs up the file with VACUUM INTO into <backupsDir> and keeps only the
// last five backups.
//
// A database whose user_version is above the highest known migration is
// refused with an explicit message.
func Open(path string, migrations fs.FS) (*DB, error) {
	all, err := listMigrations(migrations)
	if err != nil {
		return nil, fmt.Errorf("db: listing migrations: %w", err)
	}

	// modernc.org/sqlite reads only _pragma parameters; the previous
	// _journal_mode / _foreign_keys / _busy_timeout forms had no effect.
	dsn := path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	// SQLite works best with a single connection for writes.
	sqldb.SetMaxOpenConns(1)

	if err := sqldb.Ping(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("db: ping %s: %w", path, err)
	}

	version, err := userVersion(sqldb)
	if err != nil {
		sqldb.Close()
		return nil, err
	}

	maxVersion := 0
	if len(all) > 0 {
		maxVersion = all[len(all)-1].version
	}

	if version > maxVersion {
		sqldb.Close()
		return nil, fmt.Errorf("this database was written by a newer version of Sifty. Install that version or newer")
	}

	pending := pendingMigrations(all, version)
	if len(pending) == 0 {
		return &DB{DB: sqldb, Version: version}, nil
	}

	// Back up before migrating, but only when the database is not brand-new.
	isNew := version == 0 && dbIsEmpty(sqldb)
	if !isNew {
		backupsDir := filepath.Join(filepath.Dir(path), "backups")
		if err := os.MkdirAll(backupsDir, 0700); err != nil {
			sqldb.Close()
			return nil, fmt.Errorf("db: creating backups dir: %w", err)
		}
		ts := time.Now().UTC().Format("20060102T150405Z")
		backupPath := filepath.Join(backupsDir,
			fmt.Sprintf("sifty-%s-v%d.db", ts, version))
		if err := vacuumInto(sqldb, backupPath); err != nil {
			sqldb.Close()
			return nil, fmt.Errorf("db: backup before migration: %w", err)
		}
		if err := pruneBackups(backupsDir, 5); err != nil {
			// Non-fatal: log the warning but continue.
			_ = err
		}
	}

	// Run each pending migration in its own transaction, setting user_version.
	for _, m := range pending {
		if err := runMigration(sqldb, m, migrations); err != nil {
			sqldb.Close()
			return nil, fmt.Errorf("db: migration %d: %w", m.version, err)
		}
		version = m.version
	}

	return &DB{DB: sqldb, Version: version}, nil
}

// migration describes a numbered SQL migration file.
type migration struct {
	version  int
	filename string
}

var migrationRE = regexp.MustCompile(`^(\d{3,})_.*\.sql$`)

// listMigrations reads all *.sql files from the FS and returns them sorted
// by version number. Only files at the top level are considered.
func listMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationRE.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		out = append(out, migration{version: n, filename: e.Name()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// pendingMigrations returns the migrations with version > current.
func pendingMigrations(all []migration, current int) []migration {
	var out []migration
	for _, m := range all {
		if m.version > current {
			out = append(out, m)
		}
	}
	return out
}

// runMigration executes one migration SQL file inside a transaction
// and sets user_version to its number.
func runMigration(db *sql.DB, m migration, fsys fs.FS) error {
	f, err := fsys.Open(m.filename)
	if err != nil {
		return fmt.Errorf("open %s: %w", m.filename, err)
	}
	sqlBytes, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		return fmt.Errorf("read %s: %w", m.filename, err)
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	sqlText := strings.TrimSpace(string(sqlBytes))
	if sqlText != "" {
		if _, err := tx.Exec(sqlText); err != nil {
			return fmt.Errorf("exec %s: %w", m.filename, err)
		}
	}
	// user_version cannot be set through a prepared statement placeholder.
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return err
	}
	return tx.Commit()
}

// userVersion reads the SQLite user_version pragma.
func userVersion(db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("db: reading user_version: %w", err)
	}
	return v, nil
}

// dbIsEmpty returns true when the database has no user tables (i.e. is brand new).
func dbIsEmpty(db *sql.DB) bool {
	var n int
	err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`,
	).Scan(&n)
	return err == nil && n == 0
}

// vacuumInto copies the database to dst using VACUUM INTO.
func vacuumInto(db *sql.DB, dst string) error {
	_, err := db.Exec("VACUUM INTO ?", dst)
	return err
}

// pruneBackups removes the oldest backup files in dir, keeping at most keep.
func pruneBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "sifty-") && strings.HasSuffix(e.Name(), ".db") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files) // lexicographic = chronological for our timestamp format
	for len(files) > keep {
		if err := os.Remove(files[0]); err != nil && !os.IsNotExist(err) {
			return err
		}
		files = files[1:]
	}
	return nil
}
