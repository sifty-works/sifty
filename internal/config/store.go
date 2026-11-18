package config

import (
	"sync"
)

// Store holds the running configuration. All methods are safe for concurrent use.
type Store struct {
	path string
	mu   sync.RWMutex
	cfg  Config
}

// Open loads the settings file at path, validates it and returns a Store.
// A missing file is not an error; the defaults are used instead.
func Open(path string) (*Store, error) {
	cfg, err := load(path)
	if err != nil {
		return nil, err
	}
	return &Store{path: path, cfg: cfg}, nil
}

// Get returns a copy of the current configuration, safe for concurrent use.
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}
