// Package server builds and runs the HTTP server. It is responsible for:
//   - routing all requests
//   - Host and Origin enforcement (loopback-only)
//   - JSON Content-Type requirement for mutating methods
//   - 1 MB body limit
//   - typed-error → HTTP status mapping
//   - panic recovery with a unique log reference
//   - slow-request logging (>= 500 ms)
//   - static frontend serving with client-side routing fallback
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sifty-works/sifty/internal/apperr"
)

// CollectProgress describes a running collection job.
type CollectProgress struct {
	Done  int    `json:"done"`
	Total int    `json:"total"`
	Text  string `json:"text"`
}

// LastRun describes the outcome of the most recent collection run.
type LastRun struct {
	State    string `json:"state"`
	Finished string `json:"finished"`
	Message  string `json:"message"`
}

// Attention describes sources needing attention.
type Attention struct {
	Count int    `json:"count"`
	Text  string `json:"text"`
}

// StatusExtra carries the collection-specific fields merged into GET /api/status.
// It is provided by the Collector via Options.Status.
type StatusExtra struct {
	Collecting *CollectProgress `json:"collecting"`
	LastRun    *LastRun         `json:"lastRun"`
	Catalogue  int64            `json:"catalogue"`
	Attention  *Attention       `json:"attention"`
}

// StatusResp is the response body for GET /api/status.
type StatusResp struct {
	Version    string           `json:"version"`
	Offline    bool             `json:"offline"`
	Debug      bool             `json:"debug"`
	Collecting *CollectProgress `json:"collecting"`
	LastRun    *LastRun         `json:"lastRun"`
	Catalogue  int64            `json:"catalogue"`
	Attention  *Attention       `json:"attention"`
}

const (
	maxBodyBytes  = 1 << 20 // 1 MB
	slowThreshold = 500 * time.Millisecond
)

// counter generates unique IDs for internal error log entries.
var counter atomic.Uint64

// Options holds the runtime parameters the server needs.
type Options struct {
	Version string
	Port    int
	Offline bool
	Debug   bool
	Logger  *slog.Logger
	Static  fs.FS              // web/dist subtree; nil when the frontend was not built
	Status  func() StatusExtra // collection status merged into GET /api/status; nil is allowed
}

// New builds and returns the HTTP handler for the application.
// It registers all routes and wraps them in the shared middleware.
func New(opts Options, routes func(*http.ServeMux)) http.Handler {
	mux := http.NewServeMux()

	// Status endpoint.
	Handle(mux, "GET /api/status", opts.Logger, func(r *http.Request, _ struct{}) (StatusResp, error) {
		resp := StatusResp{Version: opts.Version, Offline: opts.Offline, Debug: opts.Debug}
		if opts.Status != nil {
			extra := opts.Status()
			resp.Collecting = extra.Collecting
			resp.LastRun = extra.LastRun
			resp.Catalogue = extra.Catalogue
			resp.Attention = extra.Attention
		}
		return resp, nil
	})

	// Register capability routes.
	if routes != nil {
		routes(mux)
	}

	// Unknown /api/ paths: 404 with the update message.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound,
			"Refresh the page. Sifty was updated and this address is no longer used.")
	})

	// Everything else: static frontend.
	mux.Handle("/", staticHandler(opts.Static))

	return chain(mux, opts)
}

// chain wraps handler in the shared middleware stack.
func chain(h http.Handler, opts Options) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Host check.
		if !allowedHost(r.Host, opts.Port) {
			writeError(w, http.StatusForbidden,
				"This address may only be opened from this computer.")
			return
		}

		// Origin check.
		if origin := r.Header.Get("Origin"); origin != "" {
			if !allowedOrigin(origin, opts.Port) {
				writeError(w, http.StatusForbidden,
					"This address may only be opened from this computer.")
				return
			}
		}

		// Content-Type requirement for mutating methods.
		if isMutating(r.Method) {
			ct := r.Header.Get("Content-Type")
			if !strings.HasPrefix(ct, "application/json") {
				writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json.")
				return
			}
		}

		// Body limit: reject early when Content-Length is known and over limit;
		// also wrap with MaxBytesReader as a secondary defence for chunked bodies.
		if r.ContentLength > maxBodyBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "Request body too large.")
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}

		// Panic recovery.
		rw := &responseWriter{ResponseWriter: w}
		defer func() {
			if rec := recover(); rec != nil {
				id := counter.Add(1)
				opts.Logger.Error("panic in handler",
					"id", id,
					"method", r.Method,
					"path", r.URL.Path,
					"panic", fmt.Sprintf("%v", rec),
				)
				writeError(rw, http.StatusInternalServerError,
					fmt.Sprintf("Internal error, see log #%d", id))
			}
		}()

		h.ServeHTTP(rw, r)

		elapsed := time.Since(start)
		if elapsed >= slowThreshold {
			opts.Logger.Warn("slow request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rw.written(),
				"duration", elapsed,
			)
		}
	})
}

// allowedHost reports whether host is acceptable for loopback-only access.
func allowedHost(host string, port int) bool {
	portStr := fmt.Sprintf("%d", port)
	return host == "127.0.0.1:"+portStr || host == "localhost:"+portStr
}

// allowedOrigin reports whether an Origin header value is acceptable.
func allowedOrigin(origin string, port int) bool {
	portStr := fmt.Sprintf("%d", port)
	return origin == "http://127.0.0.1:"+portStr || origin == "http://localhost:"+portStr
}

// isMutating reports whether the HTTP method changes state.
func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// staticHandler returns an http.Handler that serves files from fsys,
// falling back to index.html for client-side routing. When fsys is nil or
// has no index.html it returns a plain-text "not built" page.
func staticHandler(fsys fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fsys == nil {
			http.Error(w, "The frontend was not built. Run make build.", http.StatusOK)
			return
		}

		// Try to serve the exact file.
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		f, err := fsys.Open(path)
		if err == nil {
			f.Close()
			http.FileServerFS(fsys).ServeHTTP(w, r)
			return
		}

		// Fall back to index.html for client-side routing.
		idx, err := fsys.Open("index.html")
		if err != nil {
			http.Error(w, "The frontend was not built. Run make build.", http.StatusOK)
			return
		}
		defer idx.Close()
		content, _ := io.ReadAll(idx)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write(content) //nolint:errcheck
	})
}

// WriteJSON writes v as a JSON response with status 200.
func WriteJSON(w http.ResponseWriter, v any) {
	writeJSON(w, http.StatusOK, v)
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.Encode(v) //nolint:errcheck
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// MapError maps a typed application error to an HTTP status and writes the
// response. If err is nil it does nothing. Returns true when an error was written.
func MapError(w http.ResponseWriter, err error, logger *slog.Logger) bool {
	if err == nil {
		return false
	}
	switch e := err.(type) {
	case apperr.Invalid:
		writeError(w, http.StatusUnprocessableEntity, e.Message)
	case apperr.NotFound:
		writeError(w, http.StatusNotFound, e.Message)
	case apperr.Conflict:
		writeError(w, http.StatusConflict, e.Message)
	case apperr.Busy:
		writeError(w, http.StatusLocked, e.Message)
	default:
		id := counter.Add(1)
		logger.Error("internal error", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError,
			fmt.Sprintf("Internal error, see log #%d", id))
	}
	return true
}

// responseWriter wraps http.ResponseWriter to capture the status code for logging.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(status int) {
	rw.status = status
	rw.ResponseWriter.WriteHeader(status)
}

// written returns the recorded status code, defaulting to 200 when WriteHeader
// was never called (the implicit success case).
func (rw *responseWriter) written() int {
	if rw.status == 0 {
		return http.StatusOK
	}
	return rw.status
}
