package inbox_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sifty-works/sifty-server/internal/inbox"
	"github.com/sifty-works/sifty-server/internal/server"
)

func newIndustryServer(t *testing.T, getVocab func() []inbox.IndustryItem, saveVocab func([]inbox.IndustryItem) error) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := server.New(server.Options{Version: "test", Port: 9999, Logger: logger},
		func(mux *http.ServeMux) {
			inbox.RegisterIndustryRoutes(mux, logger, getVocab, saveVocab)
		})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func TestGetIndustries_ReturnsCurrentVocab(t *testing.T) {
	items := []inbox.IndustryItem{
		{Name: "Rocketry", Keywords: []string{"rocket propulsion", "orbital mechanics"}},
		{Name: "Viticulture", Keywords: []string{"vineyard management", "fermentation science"}},
	}
	ts := newIndustryServer(t, func() []inbox.IndustryItem { return items }, nil)
	body := apiGet(t, ts, "/api/industries")
	industries, _ := body["industries"].([]any)
	if len(industries) != 2 {
		t.Fatalf("got %d industries, want 2", len(industries))
	}
	first, _ := industries[0].(map[string]any)
	if name, _ := first["name"].(string); name != "Rocketry" {
		t.Errorf("first name: got %q, want Rocketry", name)
	}
}

func TestPutIndustries_AcceptsValidVocab(t *testing.T) {
	var saved []inbox.IndustryItem
	saveVocab := func(items []inbox.IndustryItem) error {
		saved = items
		return nil
	}
	ts := newIndustryServer(t, nil, saveVocab)

	req := map[string]any{
		"industries": []any{
			map[string]any{"name": "Aquatics", "keywords": []any{"underwater robotics", "submarine engineering"}},
		},
	}
	body, status := apiJSON(t, ts, http.MethodPut, "/api/industries", req)
	if status != http.StatusOK {
		t.Fatalf("PUT /api/industries: status %d, body %v", status, body)
	}
	if saved == nil || len(saved) != 1 || saved[0].Name != "Aquatics" {
		t.Errorf("saved vocab: got %v", saved)
	}
}

func TestPutIndustries_RejectsEmptyVocab(t *testing.T) {
	ts := newIndustryServer(t, nil, func([]inbox.IndustryItem) error { return nil })
	req := map[string]any{"industries": []any{}}
	_, status := apiJSON(t, ts, http.MethodPut, "/api/industries", req)
	if status != http.StatusUnprocessableEntity {
		t.Errorf("empty vocab: got status %d, want 422", status)
	}
}

func TestPutIndustries_RejectsEmptyName(t *testing.T) {
	ts := newIndustryServer(t, nil, func([]inbox.IndustryItem) error { return nil })
	req := map[string]any{
		"industries": []any{
			map[string]any{"name": "", "keywords": []any{"something"}},
		},
	}
	_, status := apiJSON(t, ts, http.MethodPut, "/api/industries", req)
	if status != http.StatusUnprocessableEntity {
		t.Errorf("empty name: got status %d, want 422", status)
	}
}

func TestPutIndustries_RejectsDuplicateName(t *testing.T) {
	ts := newIndustryServer(t, nil, func([]inbox.IndustryItem) error { return nil })
	req := map[string]any{
		"industries": []any{
			map[string]any{"name": "Tech", "keywords": []any{"technology"}},
			map[string]any{"name": "tech", "keywords": []any{"engineering"}},
		},
	}
	_, status := apiJSON(t, ts, http.MethodPut, "/api/industries", req)
	if status != http.StatusUnprocessableEntity {
		t.Errorf("duplicate: got status %d, want 422", status)
	}
}

func TestPutIndustries_RejectsEmptyKeywordList(t *testing.T) {
	ts := newIndustryServer(t, nil, func([]inbox.IndustryItem) error { return nil })
	req := map[string]any{
		"industries": []any{
			map[string]any{"name": "Tech", "keywords": []any{}},
		},
	}
	_, status := apiJSON(t, ts, http.MethodPut, "/api/industries", req)
	if status != http.StatusUnprocessableEntity {
		t.Errorf("empty keywords: got status %d, want 422", status)
	}
}
