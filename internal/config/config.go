// Package config loads, validates and provides defaults for the application
// settings file (<data>/sifty.yaml). Every key is optional; missing keys
// take their default values. Only the keys known in this package are allowed;
// an unknown key is an error.
package config

import (
	"bytes"
	"fmt"
	"math"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that serializes as a Go duration string in YAML,
// for example "3h" or "15m".
type Duration time.Duration

// String returns the duration in the shortest human-readable form,
// for example "3h", "1h30m", or "15m".
func (d Duration) String() string { return shortDuration(time.Duration(d)) }

// MarshalYAML renders the duration in the shortest human-readable form.
func (d Duration) MarshalYAML() (interface{}, error) {
	return shortDuration(time.Duration(d)), nil
}

// shortDuration formats d as the shortest form a user would type:
// whole hours ("3h"), whole minutes ("15m"), or hours+minutes ("1h30m").
// Durations with a sub-minute component fall back to time.Duration.String().
func shortDuration(d time.Duration) string {
	h := d / time.Hour
	rem := d % time.Hour
	m := rem / time.Minute
	if rem%time.Minute != 0 {
		return d.String()
	}
	switch {
	case h == 0:
		return fmt.Sprintf("%dm", m)
	case m == 0:
		return fmt.Sprintf("%dh", h)
	default:
		return fmt.Sprintf("%dh%dm", h, m)
	}
}

// UnmarshalYAML parses a duration string such as "3h" from YAML.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("collection.interval: invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// CollectionConfig holds the automatic collection settings.
type CollectionConfig struct {
	Auto     bool     `yaml:"auto"`
	Interval Duration `yaml:"interval"`
}

// UnmarshalYAML applies defaults for collection before decoding, so that
// keys absent from the YAML retain their default values.
func (c *CollectionConfig) UnmarshalYAML(value *yaml.Node) error {
	type rawCollection CollectionConfig
	r := &rawCollection{Auto: true, Interval: Duration(3 * time.Hour)}
	if err := value.Decode(r); err != nil {
		return err
	}
	*c = CollectionConfig(*r)
	return nil
}

// SourceConfig describes one collection source.
type SourceConfig struct {
	Name    string `yaml:"name,omitempty"`
	URL     string `yaml:"url"`
	Enabled bool   `yaml:"enabled"`
}

// UnmarshalYAML applies defaults for a source before decoding, so that
// the absent enabled key defaults to true.
func (s *SourceConfig) UnmarshalYAML(value *yaml.Node) error {
	type rawSource SourceConfig
	r := &rawSource{Enabled: true}
	if err := value.Decode(r); err != nil {
		return err
	}
	*s = SourceConfig(*r)
	return nil
}

// Config holds the application settings loaded from sifty.yaml.
// All fields are optional; Defaults returns the baseline.
type Config struct {
	Port       int              `yaml:"port"`
	Debug      bool             `yaml:"debug"`
	Collection CollectionConfig `yaml:"collection"`
	Sources    []SourceConfig   `yaml:"sources"`
	// Weights of the Fits you criteria; Rates value one unit of a currency in EUR.
	// A key that is absent keeps its default.
	Weights map[string]float64 `yaml:"weights"`
	Rates   map[string]float64 `yaml:"rates"`
}

// Defaults returns a Config with every field set to its default value.
// This is the single source of defaults for the application.
func Defaults() Config {
	return Config{
		Port:  7373,
		Debug: false,
		Collection: CollectionConfig{
			Auto:     true,
			Interval: Duration(3 * time.Hour),
		},
		Sources: []SourceConfig{},
		Weights: map[string]float64{"role": 0.25, "money": 0.20, "location": 0.075, "remote_restriction": 0.075,
			"time_zone": 0.10, "work_mode": 0.04, "engagement": 0.03, "hours": 0.03},
		Rates: map[string]float64{"EUR": 1, "USD": 0.92, "GBP": 1.17, "CAD": 0.68, "AUD": 0.60, "CHF": 1.08, "JPY": 0.0062},
	}
}

// SourceKey returns the canonical key for a source URL. Two URLs that produce
// the same key are considered duplicates. The key is the scheme and host in
// lowercase with a leading "www." stripped, the path without a trailing slash,
// the query kept, and the fragment dropped.
func SourceKey(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("url must be an absolute http or https address")
	}
	if u.User != nil {
		return "", fmt.Errorf("url must not contain credentials")
	}

	host := strings.ToLower(u.Host)
	host = strings.TrimPrefix(host, "www.")
	path := strings.TrimRight(u.Path, "/")

	key := u.Scheme + "://" + host + path
	if u.RawQuery != "" {
		key += "?" + u.RawQuery
	}
	return key, nil
}

// load reads path, parses it as YAML, merges with defaults and validates it.
// If path does not exist, the defaults are returned with no error.
func load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Defaults(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("reading settings file: %w", err)
	}
	return parse(data)
}

// parse decodes YAML bytes into a Config, rejecting unknown keys.
func parse(data []byte) (Config, error) {
	c := Defaults()

	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return c, fmt.Errorf("parsing settings file: %w", err)
	}
	if node.Kind == 0 {
		return c, nil
	}
	if node.Kind != yaml.DocumentNode || len(node.Content) == 0 {
		return c, fmt.Errorf("settings file must be a YAML mapping")
	}
	root := node.Content[0]
	if root.Kind != yaml.MappingNode {
		return c, fmt.Errorf("settings file must be a YAML mapping")
	}

	// Check for unknown top-level keys.
	knownTop := map[string]bool{
		"port": true, "debug": true, "collection": true, "sources": true, "weights": true, "rates": true,
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k := root.Content[i].Value
		if !knownTop[k] {
			return c, fmt.Errorf("unknown settings key: %q", k)
		}
	}

	// Check for unknown keys under collection:.
	knownCollection := map[string]bool{"auto": true, "interval": true}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "collection" {
			continue
		}
		collNode := root.Content[i+1]
		if collNode.Kind != yaml.MappingNode {
			break
		}
		for j := 0; j+1 < len(collNode.Content); j += 2 {
			k := collNode.Content[j].Value
			if !knownCollection[k] {
				return c, fmt.Errorf("unknown settings key: \"collection.%s\"", k)
			}
		}
	}

	// Check for unknown keys within each source entry.
	knownSource := map[string]bool{"name": true, "url": true, "enabled": true}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "sources" {
			continue
		}
		seqNode := root.Content[i+1]
		if seqNode.Kind != yaml.SequenceNode {
			break
		}
		for idx, srcNode := range seqNode.Content {
			if srcNode.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j+1 < len(srcNode.Content); j += 2 {
				k := srcNode.Content[j].Value
				if !knownSource[k] {
					return c, fmt.Errorf("unknown settings key: %q in sources[%d]", k, idx)
				}
			}
		}
	}

	if err := node.Decode(&c); err != nil {
		return c, fmt.Errorf("parsing settings file: %w", err)
	}

	if err := Validate(c); err != nil {
		return c, err
	}
	return c, nil
}

// Validate checks that the config values are in range.
// Errors are worded for the user and passed through to the UI.
func Validate(c Config) error {
	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf("port: must be between 0 and 65535, got %d", c.Port)
	}
	minInterval := Duration(15 * time.Minute)
	maxInterval := Duration(168 * time.Hour)
	if c.Collection.Interval < minInterval || c.Collection.Interval > maxInterval {
		return fmt.Errorf("The interval must be between 15 minutes and 7 days.")
	}
	defaults := Defaults().Weights
	for k, w := range c.Weights {
		if _, ok := defaults[k]; !ok || w < 0 || math.IsInf(w, 0) || math.IsNaN(w) {
			return fmt.Errorf("weights.%s: a weight of a known criterion is a number of 0 or more.", k)
		}
	}
	for k, r := range c.Rates {
		if len(k) != 3 || strings.ToUpper(k) != k || !(r > 0) || math.IsInf(r, 0) {
			return fmt.Errorf("rates.%s: a rate is a currency code and a number above 0.", k)
		}
	}
	seen := make(map[string]bool, len(c.Sources))
	for _, s := range c.Sources {
		if utf8.RuneCountInString(s.Name) > 100 {
			return fmt.Errorf("The name is at most 100 characters.")
		}
		key, err := SourceKey(s.URL)
		if err != nil {
			return fmt.Errorf("Enter an http(s) address.")
		}
		if seen[key] {
			return fmt.Errorf("This source is already in the list.")
		}
		seen[key] = true
	}
	return nil
}

// DefaultsYAML returns the default settings as a YAML file text, suitable
// for printing with 'sifty config defaults'.
func DefaultsYAML() string {
	c := Defaults()
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		panic("config: encoding defaults: " + err.Error())
	}
	_ = enc.Close()
	return buf.String()
}
