package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sifty-works/sifty-server/internal/apperr"
	"github.com/sifty-works/sifty-server/internal/config"
)

func TestOpenInvalidFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sifty.yaml")
	if err := os.WriteFile(path, []byte("unknown_key: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := config.Open(path)
	if err == nil {
		t.Fatal("Open invalid file should return an error")
	}
}

// --- Update ------------------------------------------------------------------

func openStore(t *testing.T) (*config.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := config.Open(filepath.Join(dir, "sifty.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func openStoreWith(t *testing.T, content string) (*config.Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sifty.yaml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := config.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestUpdateChangesInMemoryConfig(t *testing.T) {
	s, _ := openStore(t)
	if err := s.Update(func(c *config.Config) error { c.Port = 9999; return nil }); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if s.Get().Port != 9999 {
		t.Errorf("port after Update: got %d, want 9999", s.Get().Port)
	}
}

func TestUpdateWritesFileWithChangedKey(t *testing.T) {
	s, dir := openStore(t)
	s.Update(func(c *config.Config) error { c.Port = 9000; return nil }) //nolint:errcheck
	data, _ := os.ReadFile(filepath.Join(dir, "sifty.yaml"))
	if !strings.Contains(string(data), "port: 9000") {
		t.Errorf("file should contain 'port: 9000'; got:\n%s", data)
	}
}

func TestUpdateKeepsComments(t *testing.T) {
	s, dir := openStoreWith(t, "# my settings\nport: 7373\ndebug: false\n")
	path := filepath.Join(dir, "sifty.yaml")
	if err := s.Update(func(c *config.Config) error {
		c.Debug = true
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# my settings") {
		t.Errorf("comment should be preserved; got:\n%s", data)
	}
	if !strings.Contains(string(data), "debug: true") {
		t.Errorf("debug should be true; got:\n%s", data)
	}
	if !strings.Contains(string(data), "port: 7373") {
		t.Errorf("port should still be present; got:\n%s", data)
	}
}

func TestUpdateValidationFailureWritesNothing(t *testing.T) {
	s, dir := openStore(t)

	err := s.Update(func(c *config.Config) error {
		c.Port = 99999 // invalid
		return nil
	})
	if err == nil {
		t.Fatal("invalid update should return error")
	}
	var inv apperr.Invalid
	if !errors.As(err, &inv) {
		t.Errorf("validation error should be apperr.Invalid, got %T: %v", err, err)
	}

	// File should not exist (store was opened on missing file).
	if _, err2 := os.Stat(filepath.Join(dir, "sifty.yaml")); err2 == nil {
		t.Error("file should not have been written after failed validation")
	}
	// In-memory config unchanged.
	if s.Get().Port == 99999 {
		t.Error("in-memory config should not have changed after failed validation")
	}
}

func TestUpdateFnErrorIsInvalid(t *testing.T) {
	s, _ := openStore(t)
	err := s.Update(func(c *config.Config) error {
		return errors.New("domain rule violation")
	})
	if err == nil {
		t.Fatal("fn error should propagate")
	}
	var inv apperr.Invalid
	if !errors.As(err, &inv) {
		t.Errorf("fn error should be apperr.Invalid, got %T", err)
	}
}

func TestUpdateWritesBackup(t *testing.T) {
	s, dir := openStoreWith(t, "port: 7373\n")

	s.Update(func(c *config.Config) error { c.Port = 8000; return nil }) //nolint:errcheck

	entries, err := os.ReadDir(filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatalf("backups dir should exist: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("want 1 backup file, got %d", len(entries))
	}
}

func TestUpdateNoBackupWhenFileDidNotExist(t *testing.T) {
	s, dir := openStore(t)                                               // file does not exist yet
	s.Update(func(c *config.Config) error { c.Port = 8000; return nil }) //nolint:errcheck

	if _, err := os.Stat(filepath.Join(dir, "backups")); err == nil {
		t.Error("backups dir should not exist when file did not exist before update")
	}
}

func TestUpdateKeepsLastTenBackups(t *testing.T) {
	s, dir := openStoreWith(t, "port: 7373\n")

	// Run 12 updates so 12 backups would be written; only 10 should remain.
	for i := 8000; i < 8012; i++ {
		port := i
		s.Update(func(c *config.Config) error { //nolint:errcheck
			c.Port = port
			return nil
		})
		// Tiny sleep so timestamps differ.
		time.Sleep(time.Millisecond)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatalf("backups dir: %v", err)
	}
	if len(entries) > 10 {
		t.Errorf("want at most 10 backups, got %d", len(entries))
	}
}

func TestUpdateAddsSource(t *testing.T) {
	s, dir := openStore(t)
	s.Update(func(c *config.Config) error { //nolint:errcheck
		c.Sources = append(c.Sources, config.SourceConfig{
			URL: "https://example.com/jobs", Enabled: true,
		})
		return nil
	})

	data, _ := os.ReadFile(filepath.Join(dir, "sifty.yaml"))
	if !strings.Contains(string(data), "https://example.com/jobs") {
		t.Errorf("file should contain the new source URL; got:\n%s", data)
	}
}

func TestUpdateRemovesSource(t *testing.T) {
	s, dir := openStoreWith(t, "sources:\n  - url: https://example.com/jobs1\n  - url: https://example.com/jobs2\n")
	path := filepath.Join(dir, "sifty.yaml")

	s.Update(func(c *config.Config) error { //nolint:errcheck
		// Keep only the first source.
		c.Sources = c.Sources[:1]
		return nil
	})

	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "jobs2") {
		t.Errorf("removed source should not appear in file; got:\n%s", data)
	}
	if !strings.Contains(string(data), "jobs1") {
		t.Errorf("remaining source should still be in file; got:\n%s", data)
	}
}

func TestUpdateChangesSourceKeepsComment(t *testing.T) {
	s, dir := openStoreWith(t, "sources:\n  # job board\n  - url: https://example.com/jobs\n    enabled: true\n")
	path := filepath.Join(dir, "sifty.yaml")

	s.Update(func(c *config.Config) error { //nolint:errcheck
		c.Sources[0].Enabled = false
		return nil
	})

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# job board") {
		t.Errorf("source comment should be preserved; got:\n%s", data)
	}
	if !strings.Contains(string(data), "enabled: false") {
		t.Errorf("enabled should be false; got:\n%s", data)
	}
}

func TestUpdateFailsWhenFileIsUnparseable(t *testing.T) {
	s, dir := openStoreWith(t, "port: 7373\n")
	path := filepath.Join(dir, "sifty.yaml")

	// Externally corrupt the file (simulates a hand edit that breaks the YAML).
	garbage := []byte("{{{{: not valid yaml\n")
	os.WriteFile(path, garbage, 0600) //nolint:errcheck

	err := s.Update(func(c *config.Config) error { c.Port = 8080; return nil })
	if err == nil {
		t.Fatal("Update should fail when the file on disk is unparseable")
	}
	// File must be unchanged.
	got, _ := os.ReadFile(path)
	if string(got) != string(garbage) {
		t.Errorf("file should be unchanged after failed Update; got:\n%s", got)
	}
	// In-memory config must be unchanged.
	if s.Get().Port != 7373 {
		t.Errorf("in-memory port should still be 7373, got %d", s.Get().Port)
	}
}

func TestUpdateWritesIntervalInShortForm(t *testing.T) {
	s, dir := openStore(t)
	s.Update(func(c *config.Config) error { //nolint:errcheck
		c.Collection.Interval = config.Duration(90 * time.Minute)
		return nil
	})
	data, _ := os.ReadFile(filepath.Join(dir, "sifty.yaml"))
	if !strings.Contains(string(data), "interval: 1h30m") {
		t.Errorf("90m should serialize as 1h30m; got:\n%s", data)
	}
}

func TestUpdateWritesOnlyChangedWeights(t *testing.T) {
	s, dir := openStore(t)
	if err := s.Update(func(c *config.Config) error { c.Weights["money"] = 0.3; return nil }); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "sifty.yaml"))
	if string(data) != "weights:\n  money: 0.3\n" {
		t.Errorf("file = %q", data)
	}
	if config.Defaults().Weights["money"] != 0.20 {
		t.Error("an update changed the defaults")
	}
}
