package inbox

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/sifty-works/sifty-server/internal/apperr"
	"github.com/sifty-works/sifty-server/internal/server"
)

// IndustryItem is one industry in the vocabulary as returned or accepted by the API.
type IndustryItem struct {
	Name     string   `json:"name"`
	Keywords []string `json:"keywords"`
}

// IndustriesResp is the response for GET /api/industries and PUT /api/industries.
type IndustriesResp struct {
	Industries []IndustryItem `json:"industries"`
}

// RegisterIndustryRoutes registers GET and PUT /api/industries on mux.
// getVocab returns the current vocabulary items; saveVocab validates and
// persists a new vocabulary, returning an error on validation failure.
// Either may be nil when registering routes for schema-generation only.
func RegisterIndustryRoutes(
	mux *http.ServeMux,
	logger *slog.Logger,
	getVocab func() []IndustryItem,
	saveVocab func([]IndustryItem) error,
) {
	server.Handle(mux, "GET /api/industries", logger, func(_ *http.Request, _ struct{}) (IndustriesResp, error) {
		if getVocab == nil {
			return IndustriesResp{}, nil
		}
		return IndustriesResp{Industries: getVocab()}, nil
	})

	server.Handle(mux, "PUT /api/industries", logger, func(_ *http.Request, req IndustriesResp) (IndustriesResp, error) {
		if saveVocab == nil {
			return IndustriesResp{}, nil
		}
		if err := validateIndustryItems(req.Industries); err != nil {
			return IndustriesResp{}, err
		}
		if err := saveVocab(req.Industries); err != nil {
			return IndustriesResp{}, err
		}
		return IndustriesResp{Industries: req.Industries}, nil
	})
}

// validateIndustryItems checks that industries is non-empty, every name is
// non-empty and unique, and every industry has at least one non-empty keyword.
func validateIndustryItems(industries []IndustryItem) error {
	if len(industries) == 0 {
		return apperr.Invalid{Message: "Vocabulary must contain at least one industry."}
	}
	seen := make(map[string]bool, len(industries))
	for _, ind := range industries {
		if strings.TrimSpace(ind.Name) == "" {
			return apperr.Invalid{Message: "Industry name must not be empty."}
		}
		key := strings.ToLower(strings.TrimSpace(ind.Name))
		if seen[key] {
			return apperr.Invalid{Message: fmt.Sprintf("Duplicate industry name: %q.", ind.Name)}
		}
		seen[key] = true
		if len(ind.Keywords) == 0 {
			return apperr.Invalid{Message: fmt.Sprintf("Industry %q must have at least one keyword.", ind.Name)}
		}
		for _, kw := range ind.Keywords {
			if strings.TrimSpace(kw) == "" {
				return apperr.Invalid{Message: fmt.Sprintf("Industry %q: keyword must not be empty.", ind.Name)}
			}
		}
	}
	return nil
}
