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

func TestMap_known_url_returns_response(t *testing.T) {
	m := fetchtest.NewMap()
	m.Set("http://example.com/", fetchtest.MapEntry{
		Response: fetch.Response{
			URL:         "http://example.com/",
			Status:      200,
			ContentType: "text/html",
			Body:        []byte("<p>hello</p>"),
		},
	})

	resp, err := m.Get(context.Background(), "http://example.com/", fetch.Opt{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != 200 {
		t.Errorf("status = %d, want 200", resp.Status)
	}
	if string(resp.Body) != "<p>hello</p>" {
		t.Errorf("body = %q, want <p>hello</p>", resp.Body)
	}
}

func TestMap_unknown_url_returns_not_found(t *testing.T) {
	m := fetchtest.NewMap()
	_, err := m.Get(context.Background(), "http://example.com/missing", fetch.Opt{})
	if !errors.Is(err, fetch.ErrNotFound) {
		t.Fatalf("missing URL: got %v, want ErrNotFound", err)
	}
}

func TestMap_error_entry_returns_error(t *testing.T) {
	m := fetchtest.NewMap()
	m.Set("http://example.com/blocked", fetchtest.MapEntry{Err: fetch.ErrBlocked})

	_, err := m.Get(context.Background(), "http://example.com/blocked", fetch.Opt{})
	if !errors.Is(err, fetch.ErrBlocked) {
		t.Fatalf("error entry: got %v, want ErrBlocked", err)
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

func TestDir_unknown_url_returns_not_found_and_records(t *testing.T) {
	dir := t.TempDir()
	index := `{"entries":[]}`
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(index), 0644); err != nil {
		t.Fatal(err)
	}

	d := fetchtest.NewDir(dir)
	_, err := d.Get(context.Background(), "http://example.com/unknown", fetch.Opt{})
	if !errors.Is(err, fetch.ErrNotFound) {
		t.Fatalf("unknown URL: got %v, want ErrNotFound", err)
	}

	unknown := d.Unknown()
	if len(unknown) != 1 || unknown[0] != "http://example.com/unknown" {
		t.Errorf("Unknown() = %v, want [http://example.com/unknown]", unknown)
	}
}

func TestDir_multiple_unknown_urls_all_recorded(t *testing.T) {
	dir := t.TempDir()
	index := `{"entries":[]}`
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(index), 0644); err != nil {
		t.Fatal(err)
	}

	d := fetchtest.NewDir(dir)
	urls := []string{
		"http://example.com/a",
		"http://example.com/b",
		"http://example.com/c",
	}
	for _, u := range urls {
		d.Get(context.Background(), u, fetch.Opt{}) //nolint:errcheck
	}

	unknown := d.Unknown()
	if len(unknown) != 3 {
		t.Errorf("Unknown() has %d entries, want 3: %v", len(unknown), unknown)
	}
}
