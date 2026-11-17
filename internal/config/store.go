package config

import (
	"sync"
)

// Store holds the running configuration and notifies subscribers of changes.
// All methods are safe for concurrent use.
type Store struct {
	path string
	mu   sync.RWMutex
	cfg  Config
	subs []chan Config
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

// Subscribe returns a channel that receives the new Config after each
// successful save, and an unsubscribe function that must be called to release
// the channel. Each channel is buffered with one place and keeps only the
// latest value; a save never waits for a subscriber.
func (s *Store) Subscribe() (<-chan Config, func()) {
	ch := make(chan Config, 1)
	s.mu.Lock()
	s.subs = append(s.subs, ch)
	s.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			for i, sub := range s.subs {
				if sub == ch {
					s.subs = append(s.subs[:i], s.subs[i+1:]...)
					break
				}
			}
			close(ch)
		})
	}
	return ch, unsubscribe
}

// notify sends cfg to every subscriber without blocking. The channel keeps only
// the latest value: if a previous notification is pending, it is replaced.
// Must be called with the write lock held.
func (s *Store) notify(cfg Config) {
	for _, ch := range s.subs {
		// Drain any pending value, then send the latest.
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- cfg:
		default:
		}
	}
}
