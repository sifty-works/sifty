// Package fetchtest provides fakes for fetch.Fetcher used in tests.
package fetchtest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/sifty-works/sifty/internal/fetch"
)

// MapEntry is one entry in a Map fetcher.
type MapEntry struct {
	Response fetch.Response
	Err      error
}

// Map is a Fetcher that answers from an in-memory map of URL → entry.
// A missing URL returns fetch.ErrNotFound.
// Requests records every URL passed to Get, in order.
type Map struct {
	mu       sync.Mutex
	entries  map[string]MapEntry
	requests []string
}

// NewMap returns an empty Map fetcher.
func NewMap() *Map {
	return &Map{entries: make(map[string]MapEntry)}
}

// Set registers a URL with its response or error.
func (m *Map) Set(url string, entry MapEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[url] = entry
}

// Get implements fetch.Fetcher. It records the URL in Requests.
func (m *Map) Get(_ context.Context, url string, _ fetch.Opt) (fetch.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, url)
	e, ok := m.entries[url]
	if !ok {
		return fetch.Response{}, fetch.ErrNotFound
	}
	if e.Err != nil {
		return fetch.Response{}, e.Err
	}
	return e.Response, nil
}

// Requests returns all URLs passed to Get, in order.
func (m *Map) Requests() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.requests))
	copy(out, m.requests)
	return out
}

// dirEntry is one entry read from index.json.
type dirEntry struct {
	URL         string `json:"url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	BodyFile    string `json:"body_file"`
}

// Dir is a Fetcher that answers from a folder of recorded responses.
// The folder must contain an index.json listing each URL with its status,
// content type, and the name of a body file. The index and all body files are
// loaded at construction. A request for a URL that is not in the index returns
// fetch.ErrNotFound and records the URL so callers can assert which addresses
// were requested.
type Dir struct {
	entries map[string]fetch.Response
	mu      sync.Mutex
	unknown []string
}

// NewDir returns a Dir fetcher rooted at path. It panics if the index cannot
// be read.
func NewDir(path string) *Dir {
	entries, err := loadIndex(path)
	if err != nil {
		panic("fetchtest.NewDir: " + err.Error())
	}
	return &Dir{entries: entries}
}

// Get implements fetch.Fetcher.
func (d *Dir) Get(_ context.Context, url string, _ fetch.Opt) (fetch.Response, error) {
	if resp, ok := d.entries[url]; ok {
		return resp, nil
	}
	d.mu.Lock()
	d.unknown = append(d.unknown, url)
	d.mu.Unlock()
	return fetch.Response{}, fetch.ErrNotFound
}

// Unknown returns all URLs that were requested but not in the index.
func (d *Dir) Unknown() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.unknown))
	copy(out, d.unknown)
	return out
}

// loadIndex reads index.json from path and returns a map of URL to pre-loaded response.
func loadIndex(path string) (map[string]fetch.Response, error) {
	f, err := os.Open(filepath.Join(path, "index.json"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var idx struct{ Entries []dirEntry }
	if err := json.NewDecoder(f).Decode(&idx); err != nil {
		return nil, err
	}
	m := make(map[string]fetch.Response, len(idx.Entries))
	for _, e := range idx.Entries {
		body, err := os.ReadFile(filepath.Join(path, e.BodyFile))
		if err != nil {
			return nil, err
		}
		m[e.URL] = fetch.Response{
			URL:         e.URL,
			Status:      e.Status,
			ContentType: e.ContentType,
			Body:        body,
		}
	}
	return m, nil
}
