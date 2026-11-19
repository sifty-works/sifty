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

type MapEntry struct {
	Response fetch.Response
	Header   http.Header
	Err      error
}

type Map struct {
	OnMiss func(url string)

	mu       sync.Mutex
	entries  map[string]MapEntry
	requests []string
}

func NewMap() *Map {
	return &Map{entries: make(map[string]MapEntry)}
}

func (m *Map) Set(url string, entry MapEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[url] = entry
}

func (m *Map) Get(_ context.Context, url string, _ fetch.Opt) (fetch.Response, error) {
	m.mu.Lock()
	m.requests = append(m.requests, url)
	e, ok := m.entries[url]
	onMiss := m.OnMiss
	m.mu.Unlock()
	if !ok {
		if onMiss != nil {
			onMiss(url)
		}
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

func (m *Map) Requests() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.requests))
	copy(out, m.requests)
	return out
}

type dirEntry struct {
	URL         string `json:"url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	BodyFile    string `json:"body_file"`
}

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
