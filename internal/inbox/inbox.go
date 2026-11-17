// Package inbox handles the vacancy list endpoints.
// In this slice only the empty-list endpoint is implemented.
// Slice 2 replaces the empty answer with the real query and adds parameters.
package inbox

import (
	"encoding/json"
	"net/http"
)

const emptyText = "Inbox zero. Collect more jobs in Settings → Sources."

// Handler returns an http.Handler that handles GET /api/vacancies.
// It always returns an empty row set with the Inbox empty-state text.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"rows":  []any{},
			"empty": emptyText,
		})
	})
}
