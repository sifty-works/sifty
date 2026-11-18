package inbox

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sifty-works/sifty/internal/apperr"
	"github.com/sifty-works/sifty/internal/catalog"
	"github.com/sifty-works/sifty/internal/db"
	"github.com/sifty-works/sifty/internal/server"
)

// RegisterRoutes registers all inbox routes on mux.
// collectActive reports whether a collect job is queued or running.
func RegisterRoutes(mux *http.ServeMux, database *db.DB, now func() time.Time, logger *slog.Logger, collectActive func() bool) {
	cat := catalog.New(database, now)
	mux.Handle("GET /api/vacancies", listHandler(database, now, logger))
	mux.Handle("POST /api/vacancies/{id}/decision", decisionHandler(database, now, logger))
	mux.Handle("POST /api/vacancies/{id}/seen", seenHandler(database, logger))
	mux.Handle("DELETE /api/vacancies/{id}", deleteHandler(database, cat, now, collectActive, logger))
	mux.HandleFunc("GET /api/exclusions", handleExclusionsGet(database, logger))
	mux.HandleFunc("POST /api/exclusions", handleExclusionsPost(database, logger))
	mux.HandleFunc("DELETE /api/exclusions", handleExclusionsDelete(database, logger))
	mux.HandleFunc("GET /api/exclusions/preview", handleExclusionsPreview(database, logger))
}

// parseProfile parses the ?profile= query parameter, returning 0 when absent.
func parseProfile(req *http.Request) (int64, error) {
	p := req.URL.Query().Get("profile")
	if p == "" {
		return 0, nil
	}
	pid, err := strconv.ParseInt(p, 10, 64)
	if err != nil {
		return 0, apperr.Invalid{Message: "Invalid profile."}
	}
	return pid, nil
}

// resolveRequestIDs resolves the path id through the alias table and resolves the profile.
func resolveRequestIDs(req *http.Request, database *db.DB, profileID int64) (int64, int64, error) {
	id, err := parseID(req)
	if err != nil {
		return 0, 0, err
	}
	vacancyID, err := resolveVacancy(req.Context(), database, id)
	if err != nil {
		return 0, 0, err
	}
	pid, err := resolveProfile(req.Context(), database, profileID)
	if err != nil {
		return 0, 0, err
	}
	return vacancyID, pid, nil
}

func listHandler(database *db.DB, now func() time.Time, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		q := strings.TrimSpace(req.URL.Query().Get("q"))
		stage := req.URL.Query().Get("stage")
		if stage == "" {
			stage = "inbox"
		}
		switch stage {
		case "inbox", "shortlist", "applied", "dismissed":
		default:
			server.MapError(w, apperr.Invalid{Message: "Invalid stage."}, logger) //nolint:errcheck
			return
		}
		profileID, err := parseProfile(req)
		if server.MapError(w, err, logger) {
			return
		}
		pid, err := resolveProfile(req.Context(), database, profileID)
		if server.MapError(w, err, logger) {
			return
		}
		rows, counts, err := list(req.Context(), database, now, pid, stage, q)
		if server.MapError(w, err, logger) {
			return
		}
		type response struct {
			Rows    []Row  `json:"rows"`
			Counts  Counts `json:"counts"`
			Empty   string `json:"empty"`
			Profile int64  `json:"profile"`
		}
		server.WriteJSON(w, response{
			Rows:    rows,
			Counts:  counts,
			Empty:   emptyText(len(rows), stage, q),
			Profile: pid,
		})
	})
}

func decisionHandler(database *db.DB, now func() time.Time, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Profile   int64  `json:"profile"`
			From      string `json:"from"`
			To        string `json:"to"`
			Reason    string `json:"reason"`
			Confirmed bool   `json:"confirmed"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			server.MapError(w, apperr.Invalid{Message: "Invalid request body."}, logger) //nolint:errcheck
			return
		}
		vacancyID, pid, err := resolveRequestIDs(req, database, body.Profile)
		if server.MapError(w, err, logger) {
			return
		}
		row, counts, err := applyDecision(req.Context(), database, now, vacancyID, pid, body.From, body.To, body.Reason, body.Confirmed)
		if server.MapError(w, err, logger) {
			return
		}
		server.WriteJSON(w, map[string]any{"row": row, "counts": counts})
	})
}

func seenHandler(database *db.DB, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if server.MapError(w, err, logger) {
			return
		}
		vacancyID, err := resolveVacancy(req.Context(), database, id)
		if server.MapError(w, err, logger) {
			return
		}
		if _, err := database.ExecContext(req.Context(),
			`UPDATE vacancy SET seen_at=? WHERE id=? AND seen_at=''`,
			time.Now().UTC().Format(time.RFC3339), vacancyID); err != nil {
			server.MapError(w, err, logger) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func deleteHandler(database *db.DB, cat *catalog.Catalog, now func() time.Time, collectActive func() bool, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		profileID, err := parseProfile(req)
		if server.MapError(w, err, logger) {
			return
		}
		vacancyID, pid, err := resolveRequestIDs(req, database, profileID)
		if server.MapError(w, err, logger) {
			return
		}
		var hasDecision int
		database.QueryRowContext(req.Context(), //nolint:errcheck
			`SELECT count(*) FROM decision WHERE vacancy=? AND profile=?`, vacancyID, pid).Scan(&hasDecision)
		if hasDecision == 0 {
			server.MapError(w, apperr.Conflict{Message: "Dismiss roles in Inbox; permanent Delete is available outside Inbox"}, logger) //nolint:errcheck
			return
		}
		if collectActive != nil && collectActive() {
			server.MapError(w, apperr.Busy{Message: "Collection is running; try again when it ends."}, logger) //nolint:errcheck
			return
		}
		if err := cat.Delete(req.Context(), vacancyID); err != nil {
			server.MapError(w, err, logger) //nolint:errcheck
			return
		}
		counts, err := loadCounts(req.Context(), database, pid)
		if server.MapError(w, err, logger) {
			return
		}
		server.WriteJSON(w, map[string]any{"counts": counts})
	})
}

func parseID(req *http.Request) (int64, error) {
	id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	if err != nil {
		return 0, apperr.NotFound{Message: "Role not found."}
	}
	return id, nil
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
