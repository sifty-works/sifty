package fetchtest_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sifty-works/sifty/internal/fetch"
	"github.com/sifty-works/sifty/internal/fetch/fetchtest"
)

// --- Map ---

func TestMap(t *testing.T) {
	m := fetchtest.NewMap()
	m.Set("http://example.com/", fetchtest.MapEntry{Response: fetch.Response{
		URL: "http://example.com/", Status: 200, ContentType: "text/html", Body: []byte("<p>hello</p>"),
	}})
	m.Set("http://example.com/blocked", fetchtest.MapEntry{Err: fetch.ErrBlocked})

	resp, err := m.Get(context.Background(), "http://example.com/", fetch.Opt{})
	if err != nil || resp.Status != 200 || string(resp.Body) != "<p>hello</p>" {
		t.Errorf("known URL: err=%v status=%d body=%q", err, resp.Status, resp.Body)
	}
	if _, err := m.Get(context.Background(), "http://example.com/missing", fetch.Opt{}); !errors.Is(err, fetch.ErrNotFound) {
		t.Errorf("missing URL: got %v, want ErrNotFound", err)
	}
	if _, err := m.Get(context.Background(), "http://example.com/blocked", fetch.Opt{}); !errors.Is(err, fetch.ErrBlocked) {
		t.Errorf("error entry: got %v, want ErrBlocked", err)
	}
}

// --- Dir ---

func TestDir_known_url_returns_recorded_response(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "body.html"), []byte("<p>recorded</p>"), 0644); err != nil {
		t.Fatal(err)
	}
	index := `{"entries":[{"url":"http://example.com/","status":200,"content_type":"text/html","body_file":"body.html"}]}`
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(index), 0644); err != nil {
		t.Fatal(err)
	}

	d := fetchtest.NewDir(dir)
	resp, err := d.Get(context.Background(), "http://example.com/", fetch.Opt{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != 200 {
		t.Errorf("status = %d, want 200", resp.Status)
	}
	if string(resp.Body) != "<p>recorded</p>" {
		t.Errorf("body = %q, want <p>recorded</p>", resp.Body)
	}
}

func TestDir_unknown_urls_recorded(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(`{"entries":[]}`), 0644); err != nil {
		t.Fatal(err)
	}
	d := fetchtest.NewDir(dir)

	// A single unknown URL returns ErrNotFound and is recorded.
	if _, err := d.Get(context.Background(), "http://example.com/unknown", fetch.Opt{}); !errors.Is(err, fetch.ErrNotFound) {
		t.Fatalf("unknown URL: got %v, want ErrNotFound", err)
	}
	if u := d.Unknown(); len(u) != 1 || u[0] != "http://example.com/unknown" {
		t.Errorf("Unknown() = %v, want [http://example.com/unknown]", u)
	}

	// Multiple unknown URLs are all recorded.
	for _, u := range []string{"http://example.com/a", "http://example.com/b", "http://example.com/c"} {
		d.Get(context.Background(), u, fetch.Opt{}) //nolint:errcheck
	}
	if u := d.Unknown(); len(u) != 4 {
		t.Errorf("Unknown() has %d entries, want 4: %v", len(u), u)
	}
}
