package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sifty-works/sifty/internal/apperr"
	"gopkg.in/yaml.v3"
)

// Update applies fn to a copy of the current configuration, validates the
// result, and writes only the changed keys back to the settings file while
// preserving comments and the order of untouched keys. A backup is written
// before each change. When any step fails, nothing is written and the in-memory
// configuration is unchanged.
//
// Validation and fn errors are returned as apperr.Invalid; file I/O errors are
// returned unwrapped.
func (s *Store) Update(fn func(c *Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Apply fn to a deep copy of the current config (Sources is a slice;
	// a shallow copy would share the backing array with s.cfg).
	newCfg := s.cfg
	if len(s.cfg.Sources) > 0 {
		newCfg.Sources = make([]SourceConfig, len(s.cfg.Sources))
		copy(newCfg.Sources, s.cfg.Sources)
	}
	if err := fn(&newCfg); err != nil {
		return apperr.Invalid{Message: err.Error()}
	}

	// 2. Validate the result.
	if err := Validate(newCfg); err != nil {
		return apperr.Invalid{Message: err.Error()}
	}

	// 3. Load the current YAML node tree (or start empty).
	var rootNode *yaml.Node
	fileData, readErr := os.ReadFile(s.path)
	if readErr == nil {
		var doc yaml.Node
		if parseErr := yaml.Unmarshal(fileData, &doc); parseErr != nil {
			// The file exists but cannot be parsed. Refuse to overwrite it.
			return fmt.Errorf("current settings file cannot be parsed: %w", parseErr)
		}
		if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
			rootNode = doc.Content[0]
		}
	} else if !os.IsNotExist(readErr) {
		return fmt.Errorf("reading settings file: %w", readErr)
	}
	if rootNode == nil {
		rootNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}

	// 4. Apply only the changed keys to the node tree.
	applyDiff(rootNode, s.cfg, newCfg)

	// 5. Marshal the updated node tree.
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	docNode := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{rootNode}}
	if err := enc.Encode(docNode); err != nil {
		return fmt.Errorf("encoding settings: %w", err)
	}
	_ = enc.Close()

	// 6. Backup the current file before overwriting (skip if file did not exist).
	if readErr == nil {
		if err := writeBackup(s.path, fileData); err != nil {
			return fmt.Errorf("writing backup: %w", err)
		}
	}

	// 7. Atomic write: temp file in the same directory, then rename.
	if err := atomicWrite(s.path, buf.Bytes()); err != nil {
		return fmt.Errorf("writing settings: %w", err)
	}

	// 8. Swap the in-memory config and notify subscribers.
	s.cfg = newCfg
	s.notify(newCfg)
	return nil
}

// applyDiff updates only the nodes in root whose corresponding Config value
// changed from old to new. Existing nodes (and their comments) are left in
// place; only their scalar values are updated when needed.
func applyDiff(root *yaml.Node, old, new Config) {
	if old.Port != new.Port {
		setScalar(root, "port", fmt.Sprintf("%d", new.Port), "!!int")
	}
	if old.Debug != new.Debug {
		setScalar(root, "debug", boolStr(new.Debug), "!!bool")
	}
	if old.Collection != new.Collection {
		coll := ensureMapping(root, "collection")
		if old.Collection.Auto != new.Collection.Auto {
			setScalar(coll, "auto", boolStr(new.Collection.Auto), "!!bool")
		}
		if old.Collection.Interval != new.Collection.Interval {
			setScalar(coll, "interval",
				shortDuration(time.Duration(new.Collection.Interval)), "!!str")
		}
	}
	if !sourcesEqual(old.Sources, new.Sources) {
		updateSources(root, old.Sources, new.Sources)
	}
}

// setScalar finds the value node for key in the YAML mapping node and sets its
// value, or appends a new key-value pair when the key is absent.
func setScalar(parent *yaml.Node, key, value, tag string) {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			v := parent.Content[i+1]
			v.Kind = yaml.ScalarNode
			v.Tag = tag
			v.Value = value
			v.Content = nil
			return
		}
	}
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value},
	)
}

// ensureMapping returns the mapping child node for key, creating it if absent.
func ensureMapping(parent *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			n := parent.Content[i+1]
			if n.Kind != yaml.MappingNode {
				n.Kind = yaml.MappingNode
				n.Tag = "!!map"
				n.Value = ""
				n.Content = nil
			}
			return n
		}
	}
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		m,
	)
	return m
}

// ensureSequence returns the sequence child node for key, creating it if absent.
func ensureSequence(parent *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			n := parent.Content[i+1]
			if n.Kind != yaml.SequenceNode {
				n.Kind = yaml.SequenceNode
				n.Tag = "!!seq"
				n.Value = ""
				n.Content = nil
				n.Style = yaml.FlowStyle
			}
			return n
		}
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		seq,
	)
	return seq
}

// getMappingStr returns the string value for key in a mapping node, or "".
func getMappingStr(node *yaml.Node, key string) string {
	if node.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1].Value
		}
	}
	return ""
}

// removeMappingKey removes the key-value pair for key from a mapping node.
func removeMappingKey(node *yaml.Node, key string) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			return
		}
	}
}

// updateSources rebuilds the sources sequence node to match new, re-using
// existing nodes (preserving comments) for sources whose URL is still present.
func updateSources(root *yaml.Node, old, new []SourceConfig) {
	seq := ensureSequence(root, "sources")

	// Index existing YAML nodes by URL.
	nodeByURL := make(map[string]*yaml.Node, len(seq.Content))
	for _, item := range seq.Content {
		if u := getMappingStr(item, "url"); u != "" {
			nodeByURL[u] = item
		}
	}

	// Index old sources by URL for diffing.
	oldByURL := make(map[string]SourceConfig, len(old))
	for _, s := range old {
		oldByURL[s.URL] = s
	}

	// Build the new content in the order of new sources.
	newContent := make([]*yaml.Node, 0, len(new))
	for _, s := range new {
		if existing, ok := nodeByURL[s.URL]; ok {
			updateSourceNode(existing, oldByURL[s.URL], s)
			newContent = append(newContent, existing)
		} else {
			newContent = append(newContent, makeSourceNode(s))
		}
	}
	seq.Content = newContent

	if len(newContent) == 0 {
		seq.Style = yaml.FlowStyle
	} else {
		seq.Style = 0 // block style
	}
}

// updateSourceNode updates only the changed fields in an existing source node.
func updateSourceNode(node *yaml.Node, old, new SourceConfig) {
	if old.Name != new.Name {
		if new.Name == "" {
			removeMappingKey(node, "name")
		} else {
			setScalar(node, "name", new.Name, "!!str")
		}
	}
	if old.URL != new.URL {
		setScalar(node, "url", new.URL, "!!str")
	}
	if old.Enabled != new.Enabled {
		setScalar(node, "enabled", boolStr(new.Enabled), "!!bool")
	}
}

// makeSourceNode creates a fresh YAML mapping node for a source.
func makeSourceNode(s SourceConfig) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if s.Name != "" {
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "name"},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s.Name},
		)
	}
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "url"},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s.URL},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "enabled"},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: boolStr(s.Enabled)},
	)
	return node
}

// sourcesEqual reports whether two source slices have the same length and the
// same field values at every position.
func sourcesEqual(a, b []SourceConfig) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// boolStr returns "true" or "false".
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// writeBackup copies data to <dir>/backups/sifty-<timestamp>.yaml and
// keeps only the most recent 10 backup files.
func writeBackup(settingsPath string, data []byte) error {
	dir := filepath.Dir(settingsPath)
	backupDir := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return err
	}

	ts := time.Now().UTC().Format("20060102T150405.000000000Z")
	backupPath := filepath.Join(backupDir, "sifty-"+ts+".yaml")
	if err := os.WriteFile(backupPath, data, 0600); err != nil {
		return err
	}

	return pruneBackups(backupDir, 10)
}

// pruneBackups removes the oldest sifty-*.yaml files in dir, keeping at most
// keep files.
func pruneBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var paths []string
	for _, e := range entries {
		if !e.IsDir() &&
			strings.HasPrefix(e.Name(), "sifty-") &&
			strings.HasSuffix(e.Name(), ".yaml") {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(paths) // timestamp names sort chronologically
	for len(paths) > keep {
		os.Remove(paths[0]) //nolint:errcheck
		paths = paths[1:]
	}
	return nil
}

// atomicWrite writes data to a temporary file in the same directory as path,
// then renames it over path.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".sifty-*.yaml.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil {
		os.Remove(tmpPath) //nolint:errcheck
		return werr
	}
	if cerr != nil {
		os.Remove(tmpPath) //nolint:errcheck
		return cerr
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath) //nolint:errcheck
		return err
	}
	return nil
}
