package models

import (
	"os"
	"strings"
)

func LoadSecrets(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && k != "" {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out, nil
}
