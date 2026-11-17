package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sifty-works/sifty/internal/config"
)

// --- helpers -----------------------------------------------------------------

func writeTmp(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "sifty-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

// loadFile opens path with config.Open and returns the Config and any error.
// Tests that check parsing and validation use this instead of the removed Load.
func loadFile(t *testing.T, path string) (config.Config, error) {
	t.Helper()
	s, err := config.Open(path)
	if err != nil {
		return config.Config{}, err
	}
	return s.Get(), nil
}

// loadContent writes content to a temp file and loads it.
func loadContent(t *testing.T, content string) (config.Config, error) {
	t.Helper()
	return loadFile(t, writeTmp(t, content))
}

// --- defaults ----------------------------------------------------------------

func TestDefaultsAreValid(t *testing.T) {
	c := config.Defaults()
	if err := config.Validate(c); err != nil {
		t.Fatalf("defaults do not validate: %v", err)
	}
}

func TestDefaultsIncludeCollectionKeys(t *testing.T) {
	d := config.Defaults()
	if !d.Collection.Auto {
		t.Error("collection.auto default: want true")
	}
	if time.Duration(d.Collection.Interval) != 3*time.Hour {
		t.Errorf("collection.interval default: got %s, want 3h", time.Duration(d.Collection.Interval))
	}
	if len(d.Sources) != 0 {
		t.Errorf("sources default: want empty, got %d items", len(d.Sources))
	}
}

func TestDefaultsYAMLAccepted(t *testing.T) {
	yaml := config.DefaultsYAML()
	c, err := loadContent(t, yaml)
	if err != nil {
		t.Fatalf("config defaults YAML is rejected: %v", err)
	}
	if c.Port != config.Defaults().Port {
		t.Errorf("port: got %d, want %d", c.Port, config.Defaults().Port)
	}
	if !c.Collection.Auto {
		t.Error("collection.auto: want true after round-trip")
	}
	if time.Duration(c.Collection.Interval) != 3*time.Hour {
		t.Errorf("collection.interval: got %s, want 3h", time.Duration(c.Collection.Interval))
	}
}

// --- missing / empty file ----------------------------------------------------

func TestMissingFileReturnsDefaults(t *testing.T) {
	c, err := loadFile(t, filepath.Join(t.TempDir(), "nosuchfile.yaml"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	d := config.Defaults()
	if c.Port != d.Port || c.Debug != d.Debug ||
		c.Collection.Auto != d.Collection.Auto ||
		c.Collection.Interval != d.Collection.Interval ||
		len(c.Sources) != 0 {
		t.Errorf("missing file: got %+v, want defaults %+v", c, d)
	}
}

// --- key parsing -------------------------------------------------------------

func TestValidKeysParsed(t *testing.T) {
	parse := func(yaml string) config.Config {
		t.Helper()
		c, err := loadContent(t, yaml)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return c
	}
	if c := parse("port: 9090\n"); c.Port != 9090 {
		t.Errorf("port: got %d, want 9090", c.Port)
	}
	if c := parse("debug: true\n"); !c.Debug {
		t.Error("debug should be true")
	}
	if c := parse("collection:\n  auto: false\n"); c.Collection.Auto {
		t.Error("collection.auto: want false")
	}
	if c := parse("collection:\n  interval: 30m\n"); time.Duration(c.Collection.Interval) != 30*time.Minute {
		t.Errorf("collection.interval: got %s, want 30m", time.Duration(c.Collection.Interval))
	}
}

func TestInvalidConfigsRejected(t *testing.T) {
	cases := []struct{ yaml, errContains string }{
		{"unknown_key: true\n", "unknown_key"},
		{"port: 99999\n", "port"},
		{"collection:\n  interval: 10m\n", "The interval must be between 15 minutes and 7 days."},
		{"collection:\n  interval: 200h\n", "The interval must be between 15 minutes and 7 days."},
		{"collection:\n  unknown: true\n", "collection.unknown"},
	}
	for _, tc := range cases {
		_, err := loadContent(t, tc.yaml)
		if err == nil {
			t.Errorf("yaml %q: want error", tc.yaml)
			continue
		}
		if !strings.Contains(err.Error(), tc.errContains) {
			t.Errorf("yaml %q: error should contain %q; got: %v", tc.yaml, tc.errContains, err)
		}
	}
}

// --- sources -----------------------------------------------------------------

func TestSourceWithValidURL(t *testing.T) {
	yaml := "sources:\n  - url: https://example.com/jobs\n    enabled: true\n"
	c, err := loadContent(t, yaml)
	if err != nil {
		t.Fatalf("valid source rejected: %v", err)
	}
	if len(c.Sources) != 1 {
		t.Fatalf("want 1 source, got %d", len(c.Sources))
	}
	if c.Sources[0].URL != "https://example.com/jobs" {
		t.Errorf("url: got %q", c.Sources[0].URL)
	}
	if !c.Sources[0].Enabled {
		t.Error("enabled: want true")
	}
}

func TestSourceEnabledDefaultsToTrue(t *testing.T) {
	yaml := "sources:\n  - url: https://example.com/jobs\n"
	c, err := loadContent(t, yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !c.Sources[0].Enabled {
		t.Error("enabled should default to true when absent")
	}
}

func TestSourceInvalidURL(t *testing.T) {
	for _, bad := range []string{
		"not-a-url",
		"ftp://example.com/jobs",
		"/relative/path",
		"http://",
		"https://user:pass@example.com/jobs",
	} {
		yaml := "sources:\n  - url: " + bad + "\n"
		_, err := loadContent(t, yaml)
		if err == nil {
			t.Errorf("invalid url %q should be rejected", bad)
			continue
		}
		if !strings.Contains(err.Error(), "Enter an http(s) address.") {
			t.Errorf("error should say %q for %q: %v", "Enter an http(s) address.", bad, err)
		}
	}
}

func TestSourceNameLimitCountsCharactersNotBytes(t *testing.T) {
	// "ñ" is 2 bytes in UTF-8 but 1 character.
	// 100 ñ characters = 200 bytes: must be accepted.
	name100 := strings.Repeat("ñ", 100)
	yaml100 := "sources:\n  - name: " + name100 + "\n    url: https://example.com/jobs\n"
	if _, err := loadContent(t, yaml100); err != nil {
		t.Errorf("100-character non-ASCII name should be accepted: %v", err)
	}
	// 101 ñ characters must be rejected and name the field.
	name101 := strings.Repeat("ñ", 101)
	yaml101 := "sources:\n  - name: " + name101 + "\n    url: https://example.com/jobs\n"
	_, err := loadContent(t, yaml101)
	if err == nil {
		t.Error("101-character non-ASCII name should be rejected")
	} else if !strings.Contains(err.Error(), "The name is at most 100 characters.") {
		t.Errorf("error should say %q: %v", "The name is at most 100 characters.", err)
	}
}

func TestDuplicateSourceKey(t *testing.T) {
	// https://www.Example.com/jobs/ and https://example.com/jobs have the same key.
	yaml := `sources:
  - url: https://www.Example.com/jobs/
  - url: https://example.com/jobs
`
	_, err := loadContent(t, yaml)
	if err == nil {
		t.Fatal("duplicate source key should be rejected")
	}
	if !strings.Contains(err.Error(), "This source is already in the list.") {
		t.Errorf("error should say %q: %v", "This source is already in the list.", err)
	}
}

func TestUnknownSourceKey(t *testing.T) {
	yaml := "sources:\n  - url: https://example.com/jobs\n    mystery: true\n"
	_, err := loadContent(t, yaml)
	if err == nil {
		t.Fatal("unknown source key should be rejected")
	}
}

// --- SourceKey ---------------------------------------------------------------

func TestSourceKeyTransformations(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://EXAMPLE.COM/jobs", "https://example.com/jobs"},                 // lowercase host
		{"https://www.example.com/jobs", "https://example.com/jobs"},             // strip www
		{"https://example.com/jobs/", "https://example.com/jobs"},                // strip trailing slash
		{"https://example.com/jobs?type=go", "https://example.com/jobs?type=go"}, // keep query
		{"https://example.com/jobs#section", "https://example.com/jobs"},         // drop fragment
		{"https://www.Example.com/jobs/", "https://example.com/jobs"},            // combined
	}
	for _, tc := range cases {
		k, err := config.SourceKey(tc.in)
		if err != nil {
			t.Errorf("SourceKey(%q): unexpected error: %v", tc.in, err)
			continue
		}
		if k != tc.want {
			t.Errorf("SourceKey(%q) = %q, want %q", tc.in, k, tc.want)
		}
	}
}

func TestSourceKeyErrors(t *testing.T) {
	bad := []string{"ftp://example.com/jobs", "example.com/jobs", "/path",
		"https://user:pass@example.com/jobs"}
	for _, url := range bad {
		if _, err := config.SourceKey(url); err == nil {
			t.Errorf("SourceKey(%q) should return error", url)
		}
	}
}

// --- full round-trip ---------------------------------------------------------

func TestFullConfigRoundTrip(t *testing.T) {
	yaml := `port: 8080
collection:
  auto: false
  interval: 1h
sources:
  - name: Go Jobs
    url: https://example.com/go-jobs
    enabled: true
  - url: http://jobs.example.org/
    enabled: false
`
	c, err := loadContent(t, yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Port != 8080 {
		t.Errorf("port: got %d", c.Port)
	}
	if c.Collection.Auto {
		t.Error("collection.auto: want false")
	}
	if time.Duration(c.Collection.Interval) != time.Hour {
		t.Errorf("collection.interval: got %s", time.Duration(c.Collection.Interval))
	}
	if len(c.Sources) != 2 {
		t.Fatalf("sources: want 2, got %d", len(c.Sources))
	}
	if c.Sources[0].Name != "Go Jobs" {
		t.Errorf("sources[0].name: got %q", c.Sources[0].Name)
	}
	if c.Sources[1].URL != "http://jobs.example.org/" {
		t.Errorf("sources[1].url: got %q", c.Sources[1].URL)
	}
	if c.Sources[1].Enabled {
		t.Error("sources[1].enabled: want false")
	}
}
