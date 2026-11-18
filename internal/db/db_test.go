package db_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sifty-works/sifty-server/internal/db"
)

// fixtureFS builds an in-memory FS with the given SQL migration files.
// keys are filenames like "001_create_foo.sql".
func fixtureFS(files map[string]string) fs.FS {
	m := make(fstest.MapFS)
	for name, content := range files {
		m[name] = &fstest.MapFile{Data: []byte(content)}
	}
	return m
}

func TestPendingMigrationsRunInOrder(t *testing.T) {
	fsys := fixtureFS(map[string]string{
		"001_first.sql":  "CREATE TABLE t1 (id INTEGER PRIMARY KEY);",
		"002_second.sql": "CREATE TABLE t2 (id INTEGER PRIMARY KEY);",
	})

	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "test.db"), fsys)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	if d.Version != 2 {
		t.Errorf("version: got %d, want 2", d.Version)
	}

	// Both tables must exist.
	for _, table := range []string{"t1", "t2"} {
		var n int
		if err := d.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&n); err != nil || n == 0 {
			t.Errorf("table %s not found", table)
		}
	}
}

func TestUserVersionSetPerMigration(t *testing.T) {
	fsys := fixtureFS(map[string]string{
		"001_base.sql": "CREATE TABLE base (id INTEGER PRIMARY KEY);",
	})

	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "test.db"), fsys)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	if d.Version != 1 {
		t.Errorf("version: got %d, want 1", d.Version)
	}
}

func TestNewDatabaseGetsNoBackup(t *testing.T) {
	fsys := fixtureFS(map[string]string{
		"001_first.sql": "CREATE TABLE t (id INTEGER PRIMARY KEY);",
	})

	dir := t.TempDir()
	_, err := db.Open(filepath.Join(dir, "test.db"), fsys)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	backupsDir := filepath.Join(dir, "backups")
	if _, err := os.Stat(backupsDir); !os.IsNotExist(err) {
		t.Error("backups dir should not exist for a brand-new database")
	}
}

func TestBackupWrittenBeforeMigratingExistingDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// First open: create the database at version 1 with a table (not "new").
	fsys1 := fixtureFS(map[string]string{
		"001_first.sql": "CREATE TABLE t1 (id INTEGER PRIMARY KEY);",
	})
	d1, err := db.Open(dbPath, fsys1)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	d1.Close()

	// Second open: add a migration to trigger a backup of an existing database.
	fsys2 := fixtureFS(map[string]string{
		"001_first.sql":  "CREATE TABLE t1 (id INTEGER PRIMARY KEY);",
		"002_second.sql": "CREATE TABLE t2 (id INTEGER PRIMARY KEY);",
	})
	d2, err := db.Open(dbPath, fsys2)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer d2.Close()

	backupsDir := filepath.Join(dir, "backups")
	entries, err := os.ReadDir(backupsDir)
	if err != nil {
		t.Fatalf("backups dir not created: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 backup, got %d", len(entries))
	}
}

func TestOnlyLastFiveBackupsKept(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Build 7 versions incrementally.
	for i := 1; i <= 7; i++ {
		fsys := fixtureFS(makeMigrations(i))
		d, err := db.Open(dbPath, fsys)
		if err != nil {
			t.Fatalf("open at version %d: %v", i, err)
		}
		d.Close()
	}

	backupsDir := filepath.Join(dir, "backups")
	entries, err := os.ReadDir(backupsDir)
	if err != nil {
		t.Fatalf("backups dir: %v", err)
	}
	if len(entries) > 5 {
		t.Errorf("expected at most 5 backups, got %d", len(entries))
	}
}

func TestNewerDatabaseIsRefused(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Open with three migrations.
	fsys1 := fixtureFS(makeMigrations(3))
	d, err := db.Open(dbPath, fsys1)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	d.Close()

	// Try to open with an older migration set (version 1 < 3).
	fsys2 := fixtureFS(makeMigrations(1))
	_, err = db.Open(dbPath, fsys2)
	if err == nil {
		t.Fatal("opening a newer database should return an error")
	}
	if !strings.Contains(err.Error(), "newer version") {
		t.Errorf("error should mention 'newer version', got: %v", err)
	}
}

func TestFailingMigrationLeavesVersionUnchanged(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// First, create the database at version 1 (non-new, so it has a table).
	fsys1 := fixtureFS(makeMigrations(1))
	d1, err := db.Open(dbPath, fsys1)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	d1.Close()

	// Now try to run a failing migration.
	fsys2 := fixtureFS(map[string]string{
		"001_base.sql":   fmt.Sprintf("CREATE TABLE IF NOT EXISTS m%d (id INTEGER PRIMARY KEY);", 1),
		"002_broken.sql": "THIS IS NOT VALID SQL;",
	})
	_, err = db.Open(dbPath, fsys2)
	if err == nil {
		t.Fatal("failing migration should return an error")
	}

	// The database should still be at version 1 (rollback happened).
	fsys3 := fixtureFS(makeMigrations(1))
	d3, err := db.Open(dbPath, fsys3)
	if err != nil {
		t.Fatalf("re-open after failure: %v", err)
	}
	defer d3.Close()
	if d3.Version != 1 {
		t.Errorf("version after failed migration: got %d, want 1", d3.Version)
	}
}

// TestMigrationsFS_VacancyTableExists opens a fresh database using the
// embedded db.Migrations FS and checks that the vacancy table exists and
// that user_version is 1.
func TestEmbeddedMigrationsRunOnAnEmptyFolder(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "test.db"), db.Migrations)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	files, err := fs.Glob(db.Migrations, "*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no embedded migrations: %v", err)
	}
	if want := len(files); d.Version != want {
		t.Errorf("user_version: got %d, want %d", d.Version, want)
	}

	var n int
	if err := d.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='vacancy'`,
	).Scan(&n); err != nil || n == 0 {
		t.Error("vacancy table not found after applying db.Migrations")
	}
}

// makeMigrations generates n SQL migration files numbered 001..n.
func makeMigrations(n int) map[string]string {
	out := make(map[string]string, n)
	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("%03d_m%d.sql", i, i)
		out[name] = fmt.Sprintf("CREATE TABLE IF NOT EXISTS m%d (id INTEGER PRIMARY KEY);", i)
	}
	return out
}
