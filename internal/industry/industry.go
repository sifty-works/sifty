// Package industry provides the vocabulary of industries and the assignment
// function that maps vacancy text to a list of industry names.
package industry

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sifty-works/sifty-server/internal/listing"
	"gopkg.in/yaml.v3"
)

//go:embed vocab.yaml
var defaultVocabYAML []byte

// Industry is one entry in the vocabulary: a name and its matching keywords.
type Industry struct {
	Name     string   `yaml:"name" json:"name"`
	Keywords []string `yaml:"keywords" json:"keywords"`
}

// Vocab is the full vocabulary of industries.
type Vocab struct {
	Industries []Industry `yaml:"industries" json:"industries"`
}

// DefaultVocab returns the starting vocabulary embedded in the binary.
func DefaultVocab() Vocab {
	var v Vocab
	if err := yaml.Unmarshal(defaultVocabYAML, &v); err != nil {
		panic("industry: cannot parse embedded vocab.yaml: " + err.Error())
	}
	return v
}

// LoadOrDefault loads the vocabulary from path. When the file does not exist,
// DefaultVocab is returned. Any other error is returned to the caller.
func LoadOrDefault(path string) (Vocab, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return DefaultVocab(), nil
	}
	if err != nil {
		return Vocab{}, fmt.Errorf("industry: read vocab: %w", err)
	}
	var v Vocab
	if err := yaml.Unmarshal(data, &v); err != nil {
		return Vocab{}, fmt.Errorf("industry: parse vocab: %w", err)
	}
	return v, nil
}

// Save marshals v to YAML and writes it to path, creating parent directories
// as needed.
func Save(path string, v Vocab) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("industry: marshal vocab: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("industry: mkdir: %w", err)
		}
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("industry: write vocab: %w", err)
	}
	return nil
}

// Validate reports the first error in v: empty name, duplicate name,
// or empty keyword list.
func Validate(v Vocab) error {
	if len(v.Industries) == 0 {
		return fmt.Errorf("vocabulary must contain at least one industry")
	}
	seen := make(map[string]bool, len(v.Industries))
	for _, ind := range v.Industries {
		if strings.TrimSpace(ind.Name) == "" {
			return fmt.Errorf("industry name must not be empty")
		}
		key := strings.ToLower(strings.TrimSpace(ind.Name))
		if seen[key] {
			return fmt.Errorf("duplicate industry name: %q", ind.Name)
		}
		seen[key] = true
		if len(ind.Keywords) == 0 {
			return fmt.Errorf("industry %q must have at least one keyword", ind.Name)
		}
		for _, kw := range ind.Keywords {
			if strings.TrimSpace(kw) == "" {
				return fmt.Errorf("industry %q: keyword must not be empty", ind.Name)
			}
		}
	}
	return nil
}

// Assign returns the names of industries whose keywords appear as whole words
// or phrases in the normalised title, tags or text. The same FTSNorm
// normalisation is used as for the full-text index, so a keyword matches only
// on a word boundary.
func Assign(title, tags, text string, v Vocab) []string {
	normTitle := listing.FTSNorm(title)
	normTags := listing.FTSNorm(tags)
	normText := listing.FTSNorm(text)

	var out []string
	for _, ind := range v.Industries {
		if matchesAny(ind.Keywords, normTitle, normTags, normText) {
			out = append(out, ind.Name)
		}
	}
	return out
}

// matchesAny reports whether any keyword in kws appears as a whole word or
// phrase in any of the normalised strings normTitle, normTags, normText.
func matchesAny(kws []string, normTitle, normTags, normText string) bool {
	for _, kw := range kws {
		norm := listing.FTSNorm(kw)
		if strings.TrimSpace(norm) == "" {
			continue
		}
		if strings.Contains(normTitle, norm) ||
			strings.Contains(normTags, norm) ||
			strings.Contains(normText, norm) {
			return true
		}
	}
	return false
}
