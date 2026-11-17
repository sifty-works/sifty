// Package config loads, validates and provides defaults for the application
// settings file (<data>/sifty.yaml). Every key is optional; missing keys
// take their default values. Only the keys known in this package are allowed;
// an unknown key is an error.
package config

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config holds the application settings loaded from sifty.yaml.
// All fields are optional; Defaults returns the baseline.
type Config struct {
	Port  int  `yaml:"port"`
	Debug bool `yaml:"debug"`
}

// Defaults returns a Config with every field set to its default value.
// This is the single source of defaults for the application.
func Defaults() Config {
	return Config{
		Port:  7373,
		Debug: false,
	}
}

// Load reads path, parses it as YAML, merges with defaults and validates it.
// If path does not exist, the defaults are returned with no error.
func Load(path string) (Config, error) {
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

	// Use a node decoder so we can detect unknown keys.
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return c, fmt.Errorf("parsing settings file: %w", err)
	}
	if node.Kind == 0 {
		// Empty file: return defaults.
		return c, nil
	}
	if node.Kind != yaml.DocumentNode || len(node.Content) == 0 {
		return c, fmt.Errorf("settings file must be a YAML mapping")
	}
	root := node.Content[0]
	if root.Kind != yaml.MappingNode {
		return c, fmt.Errorf("settings file must be a YAML mapping")
	}

	// Check for unknown keys before decoding.
	known := map[string]bool{"port": true, "debug": true}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		if !known[key] {
			return c, fmt.Errorf("unknown settings key: %q", key)
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
func Validate(c Config) error {
	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf("port: must be between 0 and 65535, got %d", c.Port)
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
