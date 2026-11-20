package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type OpenResult struct {
	Broken  string
	Err     string
	Unknown []string
}

type Store struct {
	path string
	mu   sync.RWMutex
	cfg  Config
}

func Open(path string) (*Store, OpenResult, error) {
	data, readErr := os.ReadFile(path)
	if os.IsNotExist(readErr) {
		return &Store{path: path, cfg: Defaults()}, OpenResult{}, nil
	}
	if readErr != nil {
		return nil, OpenResult{}, fmt.Errorf("reading settings file: %w", readErr)
	}
	cfg, unknown, err := parse(data)
	if err == nil {
		return &Store{path: path, cfg: cfg}, OpenResult{Unknown: unknown}, nil
	}
	result := OpenResult{Err: err.Error()}
	if b := path + ".broken-" + time.Now().UTC().Format("20060102T150405"); os.Rename(path, b) == nil {
		result.Broken = filepath.Base(b)
	}
	return &Store{path: path, cfg: restoreSettings(path)}, result, nil
}

func restoreSettings(settingsPath string) Config {
	dataDir := filepath.Dir(settingsPath)
	entries, _ := os.ReadDir(filepath.Join(dataDir, "backups"))
	var paths []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "sifty-") && strings.HasSuffix(e.Name(), ".yaml") {
			paths = append(paths, filepath.Join(dataDir, "backups", e.Name()))
		}
	}
	sort.Strings(paths)
	for i := len(paths) - 1; i >= 0; i-- {
		if d, err := os.ReadFile(paths[i]); err == nil {
			if cfg, _, err := parse(d); err == nil {
				_ = atomicWrite(settingsPath, d)
				return cfg
			}
		}
	}
	_ = atomicWrite(settingsPath, []byte(DefaultsYAML()))
	return Defaults()
}

func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}
