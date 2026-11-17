package server_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sifty-works/sifty/internal/apperr"
	"github.com/sifty-works/sifty/internal/server"
)

func newTestServer(t *testing.T, routes func(*http.ServeMux)) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := server.New(server.Options{
		Version: "test",
		Port:    9999,
		Offline: false,
		Debug:   false,
		Logger:  logger,
		Static:  nil,
	}, routes)
	ts := httptest.NewServer(h)
	ts.Client().Transport = &portRewriteTransport{base: ts.Client().Transport, host: "127.0.0.1:9999"}
	t.Cleanup(ts.Close)
	return ts
}

// portRewriteTransport rewrites the Host header so the server accepts requests
// from the test client.
type portRewriteTransport struct {
	base http.RoundTripper
	host string
}

func (t *portRewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.Host = t.host
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r2)
}

func getJSON(t *testing.T, ts *httptest.Server, path string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	req.Host = "127.0.0.1:9999"
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body) //nolint:errcheck
	return resp.StatusCode, body
}

func TestStatusEndpoint(t *testing.T) {
	ts := newTestServer(t, nil)
	status, body := getJSON(t, ts, "/api/status")
	if status != http.StatusOK {
		t.Errorf("status: got %d, want 200", status)
	}
	if body["version"] != "test" {
		t.Errorf("version: got %v", body["version"])
	}
	if body["offline"] != false {
		t.Errorf("offline: got %v", body["offline"])
	}
}

func TestWrongHostRefusedWithJSON(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := server.New(server.Options{Port: 9999, Logger: logger}, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/status", nil)
	req.Host = "evil.example.com"
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("wrong host: got %d, want 403", resp.StatusCode)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body) //nolint:errcheck
	if body["error"] == "" {
		t.Error("wrong host response must be JSON with an 'error' key")
	}
}

func TestForeignOriginRefusedWithJSON(t *testing.T) {
	ts := newTestServer(t, nil)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/status", nil)
	req.Host = "127.0.0.1:9999"
	req.Header.Set("Origin", "http://evil.example.com")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign origin: got %d, want 403", resp.StatusCode)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body) //nolint:errcheck
	if body["error"] == "" {
		t.Error("foreign origin response must be JSON with an 'error' key")
	}
}

func TestAllowedOriginAccepted(t *testing.T) {
	ts := newTestServer(t, nil)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/status", nil)
	req.Host = "127.0.0.1:9999"
	req.Header.Set("Origin", "http://127.0.0.1:9999")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("allowed origin: got %d, want 200", resp.StatusCode)
	}
}

func TestPostWithoutJSONRefused(t *testing.T) {
	ts := newTestServer(t, func(mux *http.ServeMux) {
		mux.HandleFunc("POST /api/test", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
	})

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/test",
		strings.NewReader(`{"key":"val"}`))
	req.Host = "127.0.0.1:9999"
	req.Header.Set("Content-Type", "text/plain")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("non-JSON POST: got %d, want 415", resp.StatusCode)
	}
}

func TestPostWithJSONAccepted(t *testing.T) {
	ts := newTestServer(t, func(mux *http.ServeMux) {
		mux.HandleFunc("POST /api/test", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
	})

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/test",
		strings.NewReader(`{}`))
	req.Host = "127.0.0.1:9999"
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("JSON POST: got %d, want 204", resp.StatusCode)
	}
}

func TestBodyOverOneMBRefused(t *testing.T) {
	ts := newTestServer(t, func(mux *http.ServeMux) {
		mux.HandleFunc("POST /api/test", func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body) //nolint:errcheck
			w.WriteHeader(http.StatusNoContent)
		})
	})

	big := strings.Repeat("x", 1<<20+1)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/test",
		strings.NewReader(`{"data":"`+big+`"}`))
	req.Host = "127.0.0.1:9999"
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		t.Error("body > 1 MB should not return 204")
	}
}

func TestUnknownAPIPathReturns404WithMessage(t *testing.T) {
	status, body := getJSON(t, newTestServer(t, nil), "/api/no-such-path")
	if status != http.StatusNotFound {
		t.Errorf("unknown api path: got %d, want 404", status)
	}
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "Refresh the page") {
		t.Errorf("unknown api path message: got %q", msg)
	}
}

func TestPanicInHandlerReturns500WithLogID(t *testing.T) {
	ts := newTestServer(t, func(mux *http.ServeMux) {
		mux.HandleFunc("GET /api/panic", func(w http.ResponseWriter, r *http.Request) {
			panic("deliberate panic in test")
		})
	})

	status, body := getJSON(t, ts, "/api/panic")
	if status != http.StatusInternalServerError {
		t.Errorf("panic handler: got %d, want 500", status)
	}
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "Internal error, see log #") {
		t.Errorf("panic handler message: got %q", msg)
	}
	if strings.Contains(msg, "deliberate panic") {
		t.Error("Go panic text must not reach the response")
	}
}

func TestFrontendFallbackWhenNotBuilt(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := server.New(server.Options{Port: 9999, Logger: logger, Static: nil}, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	req.Host = "127.0.0.1:9999"
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "The frontend was not built") {
		t.Errorf("frontend fallback: got %q", string(body))
	}
}

func TestFrontendServesEmbeddedFiles(t *testing.T) {
	static := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(`<html>hello</html>`)},
		"app.js":     &fstest.MapFile{Data: []byte(`console.log("hi")`)},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := server.New(server.Options{Port: 9999, Logger: logger, Static: static}, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	req.Host = "127.0.0.1:9999"
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "hello") {
		t.Errorf("embedded frontend: got %q", string(body))
	}
}

func TestFrontendClientSideRoutingFallback(t *testing.T) {
	static := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(`<html>app</html>`)},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := server.New(server.Options{Port: 9999, Logger: logger, Static: static}, nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/some/deep/route", nil)
	req.Host = "127.0.0.1:9999"
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "app") {
		t.Errorf("client-side routing fallback: got %q", string(body))
	}
}

func TestErrorTypesMapToCorrectStatusCodes(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	cases := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"invalid", apperr.Invalid{Message: "bad input"}, http.StatusUnprocessableEntity},
		{"not found", apperr.NotFound{Message: "not found"}, http.StatusNotFound},
		{"conflict", apperr.Conflict{Message: "conflict"}, http.StatusConflict},
		{"busy", apperr.Busy{Message: "busy"}, http.StatusLocked},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			server.MapError(w, tc.err, logger)
			if w.Code != tc.wantStatus {
				t.Errorf("MapError(%T): got %d, want %d", tc.err, w.Code, tc.wantStatus)
			}
			var body map[string]string
			json.NewDecoder(w.Body).Decode(&body) //nolint:errcheck
			if _, ok := body["error"]; !ok {
				t.Error("MapError: response has no 'error' key")
			}
		})
	}
}
