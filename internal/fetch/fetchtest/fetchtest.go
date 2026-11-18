// Package fetchtest provides fakes for fetch.Fetcher used in tests.
package fetchtest

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/sifty-works/sifty-server/internal/fetch"
)

// MapEntry is one entry in a Map fetcher.
// Header is passed to fetch.StatusError so a recorded 403 or challenge page
// replays as ErrBlocked without needing to set Err explicitly.
type MapEntry struct {
	Response fetch.Response
	Header   http.Header
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

// Get implements fetch.Fetcher. It records the URL in Requests and applies
// fetch.StatusError so a recorded 403 or challenge page returns ErrBlocked.
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
	if err := fetch.StatusError(e.Response.Status, e.Header, e.Response.Body); err != nil {
		return fetch.Response{}, err
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

// Load reads a folder of recorded responses and returns a Map fetcher.
// The folder must contain index.json listing each URL with its status,
// content type and the name of a body file.
func Load(dir string) (*Map, error) {
	f, err := os.Open(filepath.Join(dir, "index.json"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var idx struct{ Entries []dirEntry }
	if err := json.NewDecoder(f).Decode(&idx); err != nil {
		return nil, err
	}
	m := NewMap()
	for _, e := range idx.Entries {
		body, err := os.ReadFile(filepath.Join(dir, e.BodyFile))
		if err != nil {
			return nil, err
		}
		m.Set(e.URL, MapEntry{Response: fetch.Response{
			URL:         e.URL,
			Status:      e.Status,
			ContentType: e.ContentType,
			Body:        body,
		}})
	}
	return m, nil
}
