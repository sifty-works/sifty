package inbox_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sifty-works/sifty/internal/inbox"
	"github.com/sifty-works/sifty/internal/server"
)

func TestEmptyInboxReturnsRowsAndEmptyText(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := server.New(server.Options{
		Version: "test",
		Port:    9999,
		Logger:  logger,
	}, func(mux *http.ServeMux) {
		mux.Handle("GET /api/vacancies", inbox.Handler())
	})
	ts := httptest.NewServer(h)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/vacancies", nil)
	req.Host = "127.0.0.1:9999"
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	rows, ok := body["rows"].([]any)
	if !ok {
		t.Fatalf("rows field: got %T (%v)", body["rows"], body["rows"])
	}
	if len(rows) != 0 {
		t.Errorf("rows: got %d, want 0", len(rows))
	}

	emptyText, _ := body["empty"].(string)
	if emptyText == "" {
		t.Error("empty field should not be blank")
	}
	if emptyText != "Inbox zero. Collect more jobs in Settings → Sources." {
		t.Errorf("empty text: got %q", emptyText)
	}
}
