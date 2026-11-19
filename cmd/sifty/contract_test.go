package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/sifty-works/sifty-server/internal/server"
)

// expectedRoutes lists every API route that must be in the registry.
// A failure here means a route was removed from the typed adapter (server.Handle)
// or was never registered through it.
var expectedRoutes = []string{
	"GET /api/status",
	"GET /api/version",
	"GET /api/vacancies",
	"GET /api/vacancies/{id}",
	"POST /api/vacancies/{id}/decision",
	"POST /api/vacancies/{id}/seen",
	"DELETE /api/vacancies/{id}",
	"GET /api/exclusions",
	"POST /api/exclusions",
	"DELETE /api/exclusions",
	"GET /api/exclusions/preview",
	"GET /api/sources",
	"POST /api/sources",
	"PATCH /api/sources",
	"DELETE /api/sources",
	"PUT /api/sources/schedule",
	"POST /api/collect",
	"GET /api/me",
	"PATCH /api/me",
	"PUT /api/me/photo",
	"DELETE /api/me/photo",
	"GET /api/me/photo",
	"GET /api/profiles",
	"GET /api/profiles/{id}",
	"POST /api/profiles",
	"PATCH /api/profiles/{id}",
	"DELETE /api/profiles/{id}",
	"PUT /api/active-profile",
	"GET /api/qa",
	"POST /api/qa",
	"PATCH /api/qa",
	"DELETE /api/qa",
	"GET /api/reference",
}

// TestContractAllRoutesRegistered verifies that every expected route is recorded
// in the route registry (meaning it was registered via server.Handle or
// server.HandleNoContent rather than raw mux.HandleFunc).
func TestContractAllRoutesRegistered(t *testing.T) {
	dir := t.TempDir()
	_, cancel := startServe(t, dir)
	defer cancel()

	registered := make(map[string]bool)
	for _, ri := range server.Routes() {
		registered[ri.Pattern] = true
	}
	for _, p := range expectedRoutes {
		if !registered[p] {
			t.Errorf("route missing from registry: %s", p)
		}
	}
}

// TestContractResponseShapes makes a request to each registered GET route that
// has no path parameters and validates that the JSON response body matches the
// registered response type: required fields present, no unknown fields, null
// only where the field is a pointer.
func TestContractResponseShapes(t *testing.T) {
	dir := t.TempDir()
	port, cancel := startServe(t, dir)
	defer cancel()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	cl := http.Client{}

	for _, ri := range server.Routes() {
		ri := ri
		if !strings.HasPrefix(ri.Pattern, "GET ") {
			continue
		}
		path := strings.TrimPrefix(ri.Pattern, "GET ")
		if strings.Contains(path, "{") {
			continue // skip routes that need a path parameter
		}
		if ri.RespType == nil {
			continue // no-content route
		}

		t.Run(ri.Pattern, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, base+path, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			resp, err := cl.Do(req)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s: status %d, want 200", path, resp.StatusCode)
			}

			var body map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("GET %s: decode body: %v", path, err)
			}

			for _, violation := range server.ValidateShape(ri.RespType, body, "") {
				t.Errorf("GET %s: shape: %s", path, violation)
			}
		})
	}
}

// TestContractSchemaMatchesShape verifies that for every registered response
// type, the properties genSchema puts in the OpenAPI schema are exactly the
// top-level fields ValidateShape checks. A schema property that is absent from
// the Go shape (or vice versa) would cause clients generated from the YAML to
// send or expect fields the server does not use.
func TestContractSchemaMatchesShape(t *testing.T) {
	dir := t.TempDir()
	_, cancel := startServe(t, dir)
	defer cancel()

	schemas := map[string]any{}
	for _, ri := range server.Routes() {
		if ri.RespType != nil {
			genSchema(ri.RespType, schemas)
		}
	}

	for _, ri := range server.Routes() {
		if ri.RespType == nil {
			continue
		}
		t.Run(ri.Pattern, func(t *testing.T) {
			name := ri.RespType.Name()
			s, _ := schemas[name].(map[string]any)
			props, _ := s["properties"].(map[string]any)
			if props == nil {
				return
			}
			shapeSet := make(map[string]bool)
			for _, n := range server.TopLevelFieldNames(ri.RespType) {
				shapeSet[n] = true
			}
			for k := range props {
				if !shapeSet[k] {
					t.Errorf("schema property %q is absent from Go shape", k)
				}
			}
			for n := range shapeSet {
				if _, ok := props[n]; !ok {
					t.Errorf("Go shape field %q is absent from schema", n)
				}
			}
		})
	}
}
