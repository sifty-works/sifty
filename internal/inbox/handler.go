package inbox

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/sifty-works/sifty/internal/apperr"
	"github.com/sifty-works/sifty/internal/db"
	"github.com/sifty-works/sifty/internal/server"
)

// RegisterRoutes registers all inbox routes on mux, including the vacancy list
// and all exclusion endpoints.
func RegisterRoutes(mux *http.ServeMux, database *db.DB, now func() time.Time, logger *slog.Logger) {
	mux.Handle("GET /api/vacancies", Handler(database, now, logger))
	mux.HandleFunc("GET /api/exclusions", handleExclusionsGet(database, logger))
	mux.HandleFunc("POST /api/exclusions", handleExclusionsPost(database, logger))
	mux.HandleFunc("DELETE /api/exclusions", handleExclusionsDelete(database, logger))
	mux.HandleFunc("GET /api/exclusions/preview", handleExclusionsPreview(database, logger))
}

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

func handleExclusionsGet(database *db.DB, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := queryExclusions(r.Context(), database)
		if server.MapError(w, err, logger) {
			return
		}
		server.WriteJSON(w, exclResponse(rows))
	}
}

func handleExclusionsPost(database *db.DB, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Kind  string `json:"kind"`
			Value string `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			server.MapError(w, apperr.Invalid{Message: "Invalid request body."}, logger) //nolint:errcheck
			return
		}
		rows, err := addExclusion(r.Context(), database, req.Kind, req.Value)
		if server.MapError(w, err, logger) {
			return
		}
		server.WriteJSON(w, exclResponse(rows))
	}
}

func handleExclusionsDelete(database *db.DB, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID int64 `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			server.MapError(w, apperr.Invalid{Message: "Invalid request body."}, logger) //nolint:errcheck
			return
		}
		rows, err := removeExclusion(r.Context(), database, req.ID)
		if server.MapError(w, err, logger) {
			return
		}
		server.WriteJSON(w, exclResponse(rows))
	}
}

func handleExclusionsPreview(database *db.DB, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind := r.URL.Query().Get("kind")
		value := r.URL.Query().Get("value")
		n, err := previewCount(r.Context(), database, kind, value)
		if server.MapError(w, err, logger) {
			return
		}
		text := "Would hide 0 roles"
		if n == 1 {
			text = "Would hide 1 role"
		} else if n > 1 {
			text = fmt.Sprintf("Would hide %d roles", n)
		}
		server.WriteJSON(w, map[string]string{"text": text})
	}
}
