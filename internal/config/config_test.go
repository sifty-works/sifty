package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sifty-works/sifty/internal/config"
)

func TestDefaultsAreValid(t *testing.T) {
	c := config.Defaults()
	if err := config.Validate(c); err != nil {
		t.Fatalf("defaults do not validate: %v", err)
	}
}

func TestDefaultsYAMLAccepted(t *testing.T) {
	yaml := config.DefaultsYAML()
	c, err := config.Load(writeTmp(t, yaml))
	if err != nil {
		t.Fatalf("config defaults YAML is rejected: %v", err)
	}
	if c.Port != config.Defaults().Port {
		t.Errorf("port: got %d, want %d", c.Port, config.Defaults().Port)
	}
}

func TestMissingFileReturnsDefaults(t *testing.T) {
	c, err := config.Load(filepath.Join(t.TempDir(), "nosuchfile.yaml"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if c != config.Defaults() {
		t.Errorf("missing file: got %+v, want defaults %+v", c, config.Defaults())
	}
}

func TestPortOverride(t *testing.T) {
	c, err := config.Load(writeTmp(t, "port: 9090\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Port != 9090 {
		t.Errorf("port: got %d, want 9090", c.Port)
	}
}

func TestUnknownKeyIsError(t *testing.T) {
	_, err := config.Load(writeTmp(t, "unknown_key: true\n"))
	if err == nil {
		t.Fatal("unknown key should return an error")
	}
	if !strings.Contains(err.Error(), "unknown_key") {
		t.Errorf("error should name the unknown key, got: %v", err)
	}
}

func TestInvalidPortIsError(t *testing.T) {
	_, err := config.Load(writeTmp(t, "port: 99999\n"))
	if err == nil {
		t.Fatal("invalid port should return an error")
	}
	if !strings.Contains(err.Error(), "port") {
		t.Errorf("error should name the key 'port', got: %v", err)
	}
}

func TestDebugKey(t *testing.T) {
	c, err := config.Load(writeTmp(t, "debug: true\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !c.Debug {
		t.Error("debug should be true")
	}
}

func TestEmptyFileReturnsDefaults(t *testing.T) {
	c, err := config.Load(writeTmp(t, ""))
	if err != nil {
		t.Fatalf("empty file should not error: %v", err)
	}
	if c != config.Defaults() {
		t.Errorf("empty file: got %+v, want defaults %+v", c, config.Defaults())
	}
}

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
