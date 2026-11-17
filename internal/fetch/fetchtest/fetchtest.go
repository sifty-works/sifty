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

// dirIndex is the JSON structure of index.json in a Dir fixture folder.
type dirIndex struct {
	Entries []dirEntry `json:"entries"`
}

type dirEntry struct {
	URL         string `json:"url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	BodyFile    string `json:"body_file"`
}

// Dir is a Fetcher that answers from a folder of recorded responses.
// The folder must contain an index.json listing each URL with its status,
// content type, and the name of a body file. A request for a URL that is
// not in the index returns fetch.ErrNotFound and records the URL so callers
// can assert which addresses were requested.
type Dir struct {
	path string

	mu      sync.Mutex
	unknown []string
}

// NewDir returns a Dir fetcher rooted at path.
func NewDir(path string) *Dir {
	return &Dir{path: path}
}

// Get implements fetch.Fetcher.
func (d *Dir) Get(_ context.Context, url string, _ fetch.Opt) (fetch.Response, error) {
	idx, err := d.loadIndex()
	if err != nil {
		return fetch.Response{}, err
	}
	for _, e := range idx.Entries {
		if e.URL == url {
			body, err := os.ReadFile(filepath.Join(d.path, e.BodyFile))
			if err != nil {
				return fetch.Response{}, err
			}
			return fetch.Response{
				URL:         url,
				Status:      e.Status,
				ContentType: e.ContentType,
				Body:        body,
			}, nil
		}
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

func (d *Dir) loadIndex() (*dirIndex, error) {
	f, err := os.Open(filepath.Join(d.path, "index.json"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var idx dirIndex
	if err := json.NewDecoder(f).Decode(&idx); err != nil {
		return nil, err
	}
	return &idx, nil
}
