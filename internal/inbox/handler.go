package inbox

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sifty-works/sifty-server/internal/apperr"
	"github.com/sifty-works/sifty-server/internal/catalog"
	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/server"
)

// --- Request and response types ---

// ListReq carries the query parameters for GET /api/vacancies.
type ListReq struct {
	Stage       Stage  `json:"stage"`
	Profile     int64  `json:"profile"`
	Q           string `json:"q"`
	Industries  string `json:"industries,omitempty"`
	Collected   string `json:"collected,omitempty"`   // "7" or "30" days by first_seen; empty = any
	HideExpired bool   `json:"hideExpired,omitempty"` // hide vacancies past their expiry date
	Remote      bool   `json:"remote,omitempty"`      // remote/anywhere/telecommute in location
	Leadership  bool   `json:"leadership,omitempty"`  // CTO/Head/Director/Manager/Lead in title
	Sources     string `json:"sources,omitempty"`     // comma-separated source keys
	Unread      bool   `json:"unread,omitempty"`      // hide read vacancies; ignored in applied/dismissed
}

// ListResp is the response body for GET /api/vacancies.
type ListResp struct {
	Rows           []Row           `json:"rows"`
	Counts         Counts          `json:"counts"`
	IndustryCounts []IndustryCount `json:"industryCounts"`
	SourceCounts   []SourceCount   `json:"sourceCounts"`
	Total          int             `json:"total"`
	Empty          string          `json:"empty"`
	Profile        int64           `json:"profile"`
}

// VacancyViewReq carries the query parameters for GET /api/vacancies/{id}.
type VacancyViewReq struct {
	Profile int64 `json:"profile"`
}

// DecisionReq is the request body for POST /api/vacancies/{id}/decision.
type DecisionReq struct {
	Profile   int64  `json:"profile"`
	From      Stage  `json:"from"`
	To        Stage  `json:"to"`
	Reason    string `json:"reason"`
	Confirmed bool   `json:"confirmed"`
}

// DecisionResp is the response body for POST /api/vacancies/{id}/decision.
type DecisionResp struct {
	Row    Row    `json:"row"`
	Counts Counts `json:"counts"`
}

// DeleteVacancyResp is the response body for DELETE /api/vacancies/{id}.
type DeleteVacancyResp struct {
	Counts Counts `json:"counts"`
}

// AddExclusionReq is the request body for POST /api/exclusions.
type AddExclusionReq struct {
	Kind    ExclusionKind `json:"kind"`
	Value   string        `json:"value"`
	Profile int64         `json:"profile,omitempty"`
}

// DeleteExclusionReq is the request body for DELETE /api/exclusions.
type DeleteExclusionReq struct {
	ID      int64 `json:"id"`
	Profile int64 `json:"profile,omitempty"`
}

// ExclusionListReq carries the query parameters for GET /api/exclusions.
type ExclusionListReq struct {
	Profile int64 `json:"profile,omitempty"`
}

// PreviewReq carries the query parameters for GET /api/exclusions/preview.
type PreviewReq struct {
	Kind  ExclusionKind `json:"kind"`
	Value string        `json:"value"`
}

// PreviewResp is the response body for GET /api/exclusions/preview.
type PreviewResp struct {
	Text string `json:"text"`
}

// RegisterRoutes registers all inbox routes on mux.
// collectActive reports whether a collect job is queued or running.
// sourceNames maps source keys (from config.SourceKey) to their display names.
// getVocabNames returns the current vocabulary's industry names; nil means no
// vocabulary is configured and target-industry filtering is not applied.
func RegisterRoutes(mux *http.ServeMux, database *db.DB, now func() time.Time, logger *slog.Logger, collectActive func() bool, sourceNames map[string]string, getVocabNames ...func() []string) {
	cat := catalog.New(database, now)
	var vocabNamesFunc func() []string
	if len(getVocabNames) > 0 {
		vocabNamesFunc = getVocabNames[0]
	}
	vocabNames := func() []string {
		if vocabNamesFunc == nil {
			return nil
		}
		return vocabNamesFunc()
	}

	server.Handle(mux, "GET /api/vacancies", logger, func(r *http.Request, req ListReq) (ListResp, error) {
		stage := strings.TrimSpace(string(req.Stage))
		if stage == "" {
			stage = "inbox"
		}
		switch stage {
		case "inbox", "shortlist", "applied", "dismissed":
		default:
			return ListResp{}, apperr.Invalid{Message: "Invalid stage."}
		}
		pid, err := resolveProfile(r.Context(), database, req.Profile)
		if err != nil {
			return ListResp{}, err
		}
		var industries []string
		if req.Industries != "" {
			for _, ind := range strings.Split(req.Industries, ",") {
				if s := strings.TrimSpace(ind); s != "" {
					industries = append(industries, s)
				}
			}
		}
		var sources []string
		if req.Sources != "" {
			for _, s := range strings.Split(req.Sources, ",") {
				if s = strings.TrimSpace(s); s != "" {
					sources = append(sources, s)
				}
			}
		}
		collected := 0
		switch req.Collected {
		case "7":
			collected = 7
		case "30":
			collected = 30
		}
		// unread is ignored in applied and dismissed (Archive).
		unread := req.Unread && stage != "applied" && stage != "dismissed"
		f := Filters{
			Collected:   collected,
			HideExpired: req.HideExpired,
			Remote:      req.Remote,
			Leadership:  req.Leadership,
			Sources:     sources,
			Unread:      unread,
		}
		rows, counts, indCounts, srcCounts, total, err := list(r.Context(), database, now, pid, stage, strings.TrimSpace(req.Q), vocabNames(), industries, f)
		if err != nil {
			return ListResp{}, err
		}
		if indCounts == nil {
			indCounts = []IndustryCount{}
		}
		if srcCounts == nil {
			srcCounts = []SourceCount{}
		}
		hasFilters := req.Q != "" || len(industries) > 0 || len(sources) > 0 ||
			collected != 0 || req.HideExpired || req.Remote || req.Leadership || unread
		return ListResp{
			Rows:           rows,
			Counts:         counts,
			IndustryCounts: indCounts,
			SourceCounts:   srcCounts,
			Total:          total,
			Empty:          emptyText(len(rows), stage, hasFilters),
			Profile:        pid,
		}, nil
	})

	server.Handle(mux, "GET /api/vacancies/{id}", logger, func(r *http.Request, req VacancyViewReq) (VacancyView, error) {
		pid, err := resolveProfile(r.Context(), database, req.Profile)
		if err != nil {
			return VacancyView{}, err
		}
		id, err := parseID(r)
		if err != nil {
			return VacancyView{}, err
		}
		vacancyID, err := resolveVacancy(r.Context(), database, id)
		if err != nil {
			return VacancyView{}, err
		}
		return vacancyView(r.Context(), database, now, vacancyID, pid, sourceNames)
	})

	server.Handle(mux, "POST /api/vacancies/{id}/decision", logger, func(r *http.Request, req DecisionReq) (DecisionResp, error) {
		vacancyID, pid, err := resolveRequestIDs(r, database, req.Profile)
		if err != nil {
			return DecisionResp{}, err
		}
		row, counts, err := applyDecision(r.Context(), database, now, vacancyID, pid, string(req.From), string(req.To), req.Reason, req.Confirmed, vocabNames())
		if err != nil {
			return DecisionResp{}, err
		}
		return DecisionResp{Row: row, Counts: counts}, nil
	})

	server.Handle(mux, "POST /api/vacancies/{id}/seen", logger, func(r *http.Request, _ struct{}) (struct{}, error) {
		id, err := parseID(r)
		if err != nil {
			return struct{}{}, err
		}
		vacancyID, err := resolveVacancy(r.Context(), database, id)
		if err != nil {
			return struct{}{}, err
		}
		_, err = database.ExecContext(r.Context(),
			`UPDATE vacancy SET seen_at=? WHERE id=? AND seen_at=''`,
			time.Now().UTC().Format(time.RFC3339), vacancyID)
		return struct{}{}, err
	})

	server.Handle(mux, "DELETE /api/vacancies/{id}", logger, func(r *http.Request, _ struct{}) (DeleteVacancyResp, error) {
		profileID, err := parseProfileQuery(r)
		if err != nil {
			return DeleteVacancyResp{}, err
		}
		vacancyID, pid, err := resolveRequestIDs(r, database, profileID)
		if err != nil {
			return DeleteVacancyResp{}, err
		}
		var hasDecision int
		database.QueryRowContext(r.Context(), //nolint:errcheck
			`SELECT count(*) FROM decision WHERE vacancy=? AND profile=?`, vacancyID, pid).Scan(&hasDecision)
		if hasDecision == 0 {
			return DeleteVacancyResp{}, apperr.Conflict{Message: "Dismiss roles in Inbox; permanent Delete is available outside Inbox"}
		}
		if collectActive != nil && collectActive() {
			return DeleteVacancyResp{}, apperr.Busy{Message: "Collection is running; try again when it ends."}
		}
		if err := cat.Delete(r.Context(), vacancyID); err != nil {
			return DeleteVacancyResp{}, err
		}
		counts, err := loadCounts(r.Context(), database, pid, vocabNames())
		if err != nil {
			return DeleteVacancyResp{}, err
		}
		return DeleteVacancyResp{Counts: counts}, nil
	})

	server.Handle(mux, "GET /api/exclusions", logger, func(r *http.Request, req ExclusionListReq) (ExclusionsResp, error) {
		rows, err := queryExclusions(r.Context(), database, req.Profile)
		if err != nil {
			return ExclusionsResp{}, err
		}
		return toExclusionsResp(rows, req.Profile), nil
	})

	server.Handle(mux, "POST /api/exclusions", logger, func(r *http.Request, req AddExclusionReq) (ExclusionsResp, error) {
		rows, err := addExclusion(r.Context(), database, string(req.Kind), req.Value, req.Profile)
		if err != nil {
			return ExclusionsResp{}, err
		}
		return toExclusionsResp(rows, req.Profile), nil
	})

	server.Handle(mux, "DELETE /api/exclusions", logger, func(r *http.Request, req DeleteExclusionReq) (ExclusionsResp, error) {
		rows, err := removeExclusion(r.Context(), database, req.ID, req.Profile)
		if err != nil {
			return ExclusionsResp{}, err
		}
		return toExclusionsResp(rows, req.Profile), nil
	})

	server.Handle(mux, "GET /api/exclusions/preview", logger, func(r *http.Request, req PreviewReq) (PreviewResp, error) {
		n, err := previewCount(r.Context(), database, string(req.Kind), req.Value)
		if err != nil {
			return PreviewResp{}, err
		}
		text := "Would hide 0 roles"
		if n == 1 {
			text = "Would hide 1 role"
		} else if n > 1 {
			text = fmt.Sprintf("Would hide %d roles", n)
		}
		return PreviewResp{Text: text}, nil
	})
}

// parseID parses the {id} path value.
func parseID(req *http.Request) (int64, error) {
	id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	if err != nil {
		return 0, apperr.NotFound{Message: "Role not found."}
	}
	return id, nil
}

// parseProfileQuery reads the ?profile= query parameter, returning 0 when absent.
func parseProfileQuery(req *http.Request) (int64, error) {
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
