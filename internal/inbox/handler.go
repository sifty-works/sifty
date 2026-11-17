package inbox

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/sifty-works/sifty/internal/db"
	"github.com/sifty-works/sifty/internal/server"
)

// Handler returns an http.Handler for GET /api/vacancies.
// database is the catalog database; now is called each time the current time is needed.
func Handler(database *db.DB, now func() time.Time, logger *slog.Logger) http.Handler {
	r := newRepo(database)
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		q := strings.TrimSpace(req.URL.Query().Get("q"))
		rows, total, err := list(req.Context(), r, now, q)
		if server.MapError(w, err, logger) {
			return
		}
		type response struct {
			Rows  []Row  `json:"rows"`
			Total int    `json:"total"`
			Empty string `json:"empty"`
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response{ //nolint:errcheck
			Rows:  rows,
			Total: total,
			Empty: emptyText(len(rows), q),
		})
	})
}
