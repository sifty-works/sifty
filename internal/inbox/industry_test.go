package inbox_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sifty-works/sifty-server/internal/inbox"
	"github.com/sifty-works/sifty-server/internal/industry"
	"github.com/sifty-works/sifty-server/internal/server"
)

func newIndustryServer(t *testing.T) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := server.New(server.Options{Version: "test", Port: 9999, Logger: logger},
		func(mux *http.ServeMux) {
			inbox.RegisterIndustryRoutes(mux, logger)
		})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func TestGetIndustries_ListsFixedNames(t *testing.T) {
	ts := newIndustryServer(t)
	body := apiGet(t, ts, "/api/industries")
	industries, _ := body["industries"].([]any)
	names := industry.Names()
	if len(industries) != len(names) {
		t.Fatalf("got %d industries, want %d", len(industries), len(names))
	}
	first, _ := industries[0].(map[string]any)
	if name, _ := first["name"].(string); name != names[0] {
		t.Errorf("first name: got %q, want %q", name, names[0])
	}
}
