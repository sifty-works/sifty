package source_test

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sifty-works/sifty-server/internal/source"
)

func writeSitesYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sites.yaml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func resetSites(t *testing.T) {
	t.Helper()
	path := writeSitesYAML(t, "sites: []\n")
	source.LoadSites(path)
}

func TestEmbeddedSitesGiveCorrectModule(t *testing.T) {
	for _, tc := range []struct {
		host string
		want string
	}{
		{"web3.career", "links"},
		{"web3.careers", "links"},
		{"remote3.co", "feed"},
		{"weworkremotely.com", "feed"},
		{"cryptocurrencyjobs.co", "feed"},
		{"jobs.framework.ventures", "getro"},
		{"a16zfrontier.com", "a16z"},
		{"paradigm.xyz", "paradigm"},
	} {
		t.Run(tc.host, func(t *testing.T) {
			u, _ := url.Parse("https://" + tc.host + "/")
			mod, _ := source.For(u)
			if mod.Name() != tc.want {
				t.Errorf("For(%q).Name() = %q, want %q", tc.host, mod.Name(), tc.want)
			}
		})
	}
}

func TestLoadSitesValidOverrideReplacesEntry(t *testing.T) {
	t.Cleanup(func() { resetSites(t) })
	path := writeSitesYAML(t, "sites:\n  - host: web3.career\n    module: feed\n    list_address: https://web3.career/rss\n")
	broken, err := source.LoadSites(path)
	if err != nil || broken != "" {
		t.Fatalf("valid override should produce no recovery, got broken=%q err=%v", broken, err)
	}
	u, _ := url.Parse("https://web3.career/")
	mod, site := source.For(u)
	if mod.Name() != "feed" {
		t.Errorf("module = %q, want feed after override", mod.Name())
	}
	if site.ListAddress != "https://web3.career/rss" {
		t.Errorf("list_address = %q", site.ListAddress)
	}
}

func TestLoadSitesOverrideAddsNewHostAndKeepsEmbedded(t *testing.T) {
	t.Cleanup(func() { resetSites(t) })
	path := writeSitesYAML(t, "sites:\n  - host: newboard.example.com\n    module: feed\n    list_address: https://newboard.example.com/rss\n")
	broken, err := source.LoadSites(path)
	if err != nil || broken != "" {
		t.Fatalf("valid override should produce no recovery, got broken=%q err=%v", broken, err)
	}
	newU, _ := url.Parse("https://newboard.example.com/")
	if mod, _ := source.For(newU); mod.Name() != "feed" {
		t.Errorf("new host module = %q, want feed", mod.Name())
	}
	embU, _ := url.Parse("https://web3.career/")
	if mod, _ := source.For(embU); mod.Name() != "links" {
		t.Errorf("embedded host = %q after override, want links unchanged", mod.Name())
	}
}

func TestLoadSitesUnknownModuleRenamesFileAndKeepsEmbedded(t *testing.T) {
	path := writeSitesYAML(t, "sites:\n  - host: board.example.com\n    module: badmodule\n")
	broken, err := source.LoadSites(path)
	if err == nil {
		t.Fatal("unknown module should produce a recovery")
	}
	if broken == "" {
		t.Error("file with unknown module should be renamed")
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("original path should not exist after rename")
	}
	u, _ := url.Parse("https://web3.career/")
	if mod, _ := source.For(u); mod.Name() != "links" {
		t.Errorf("embedded module = %q after rejected override, want links", mod.Name())
	}
}

func TestLoadSitesBadLinkPatternRenamesFile(t *testing.T) {
	path := writeSitesYAML(t, "sites:\n  - host: board.example.com\n    module: links\n    link_pattern: )(\n")
	broken, err := source.LoadSites(path)
	if err == nil {
		t.Fatal("bad link_pattern should produce a recovery")
	}
	if broken == "" {
		t.Error("file with bad link_pattern should be renamed")
	}
	if !strings.Contains(err.Error(), "link_pattern") {
		t.Errorf("error should mention link_pattern: %s", err)
	}
}

func TestLoadSitesDuplicateHostRenamesFile(t *testing.T) {
	path := writeSitesYAML(t, "sites:\n  - host: board.example.com\n    module: feed\n  - host: board.example.com\n    module: links\n")
	broken, err := source.LoadSites(path)
	if err == nil {
		t.Fatal("duplicate host should produce a recovery")
	}
	if broken == "" {
		t.Error("file with duplicate host should be renamed")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error should mention duplicate: %s", err)
	}
}

func TestLoadSites_UnknownIndustryRenamesFile(t *testing.T) {
	path := writeSitesYAML(t, "sites:\n  - host: board.example.com\n    module: feed\n    industries:\n      - NoSuchIndustry\n")
	broken, err := source.LoadSites(path)
	if err == nil {
		t.Fatal("unknown industry should produce a recovery")
	}
	if broken == "" {
		t.Error("file with unknown industry should be renamed")
	}
	if !strings.Contains(err.Error(), "unknown industry") {
		t.Errorf("error should mention 'unknown industry': %s", err)
	}
}
