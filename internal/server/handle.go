package server

import (
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/sifty-works/sifty-server/internal/apperr"
)

// RouteInfo records the types registered for one API route.
type RouteInfo struct {
	Pattern  string
	ReqType  reflect.Type // nil for no-request or struct{} requests
	RespType reflect.Type // nil for no-content responses
}

var (
	routeMu  sync.Mutex
	routeMap = map[string]RouteInfo{}
)

// Routes returns a copy of all registered routes, sorted by pattern.
func Routes() []RouteInfo {
	routeMu.Lock()
	defer routeMu.Unlock()
	out := slices.Collect(maps.Values(routeMap))
	slices.SortFunc(out, func(a, b RouteInfo) int { return strings.Compare(a.Pattern, b.Pattern) })
	return out
}

// ResetRoutes clears the registry. Called at the start of gen-api.
func ResetRoutes() {
	routeMu.Lock()
	defer routeMu.Unlock()
	routeMap = map[string]RouteInfo{}
}

// normType returns the reflect.Type for T, or nil when T is an empty struct
// (meaning the route has no request body or returns no content).
func normType[T any]() (t reflect.Type) {
	if t = reflect.TypeOf(*new(T)); t != nil && t.Kind() == reflect.Struct && t.NumField() == 0 {
		t = nil
	}
	return
}

// HandleRaw registers fn at pattern in the route registry without JSON decoding.
// The function is responsible for reading the request body and writing the response.
// Used for binary uploads and binary responses where the body is not JSON.
func HandleRaw(mux *http.ServeMux, pattern string, fn http.HandlerFunc) {
	routeMu.Lock()
	routeMap[pattern] = RouteInfo{Pattern: pattern}
	routeMu.Unlock()
	mux.HandleFunc(pattern, fn)
}

// Handle registers fn at pattern and records its request and response types in
// the global registry. For GET requests Req is decoded from query parameters
// by json tag name; for all other methods Req is decoded from the JSON body.
// When Resp is struct{} the response is 204 No Content with no body.
func Handle[Req, Resp any](mux *http.ServeMux, pattern string, log *slog.Logger, fn func(*http.Request, Req) (Resp, error)) {
	reqType, rspType := normType[Req](), normType[Resp]()
	routeMu.Lock()
	routeMap[pattern] = RouteInfo{Pattern: pattern, ReqType: reqType, RespType: rspType}
	routeMu.Unlock()
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeReq[Req](pattern, r)
		if MapError(w, err, log) {
			return
		}
		resp, err := fn(r, req)
		if MapError(w, err, log) {
			return
		}
		if rspType == nil {
			w.WriteHeader(http.StatusNoContent)
		} else {
			writeJSON(w, http.StatusOK, resp)
		}
	})
}

// decodeReq decodes request parameters into T. For GET the json-tagged fields
// are read from query parameters; for all other methods from the JSON body.
func decodeReq[T any](pattern string, r *http.Request) (T, error) {
	if strings.HasPrefix(pattern, "GET ") {
		return decodeQuery[T](r)
	}
	return decodeBody[T](r)
}

// decodeQuery reads struct fields from r's URL query parameters by json tag name.
func decodeQuery[T any](r *http.Request) (T, error) {
	var v T
	rv := reflect.ValueOf(&v).Elem()
	if rv.Kind() != reflect.Struct {
		return v, nil
	}
	q, t := r.URL.Query(), rv.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		name := JSONTagName(f)
		if name == "" || name == "-" {
			continue
		}
		s := q.Get(name)
		if s == "" {
			continue
		}
		fv := rv.Field(i)
		switch fv.Kind() {
		case reflect.String:
			fv.SetString(s)
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return v, apperr.Invalid{Message: "Invalid " + name + "."}
			}
			fv.SetInt(n)
		case reflect.Bool:
			b, err := strconv.ParseBool(s)
			if err != nil {
				return v, apperr.Invalid{Message: "Invalid " + name + "."}
			}
			fv.SetBool(b)
		}
	}
	return v, nil
}

// decodeBody decodes a JSON body into T. An empty or null body returns a zero T.
func decodeBody[T any](r *http.Request) (T, error) {
	var v T
	if r.Body == nil || r.ContentLength == 0 {
		return v, nil
	}
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		return v, apperr.Invalid{Message: "Invalid request body."}
	}
	return v, nil
}

// JSONTagName returns the JSON key name for a struct field.
func JSONTagName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "" {
		return strings.ToLower(f.Name)
	}
	name, _, _ := strings.Cut(tag, ",")
	return name
}

// deref follows pointer indirections to their base type.
func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t
}

// ValidateShape checks that body matches the JSON shape of typ: all required
// fields (those without omitempty) must be present; no unknown keys; null only
// for pointer fields. Recurses into nested structs and slice elements.
// Returns a list of violations (each prefixed with the field path), empty when valid.
func ValidateShape(typ reflect.Type, body map[string]any, prefix string) []string {
	if typ == nil {
		return nil
	}
	typ = deref(typ)
	if typ.Kind() != reflect.Struct {
		return nil
	}
	var errs []string
	known := make(map[string]bool)
	for i := range typ.NumField() {
		f := typ.Field(i)
		n := JSONTagName(f)
		if n == "" || n == "-" {
			continue
		}
		known[n] = true
		p := prefix + n
		val, ok := body[n]
		if !ok {
			if !strings.Contains(f.Tag.Get("json"), "omitempty") {
				errs = append(errs, "required field missing: "+p)
			}
			continue
		}
		if val == nil {
			if f.Type.Kind() != reflect.Ptr {
				errs = append(errs, "unexpected null: "+p)
			}
			continue
		}
		if ft := deref(f.Type); ft.Kind() == reflect.Struct {
			if m, ok := val.(map[string]any); ok {
				errs = append(errs, ValidateShape(ft, m, p+".")...)
			}
		} else if ft.Kind() == reflect.Slice {
			if et := deref(ft.Elem()); et.Kind() == reflect.Struct {
				if vs, ok := val.([]any); ok {
					for j, v := range vs {
						if m, ok := v.(map[string]any); ok {
							errs = append(errs, ValidateShape(et, m, p+"["+strconv.Itoa(j)+"].")...)
						}
					}
				}
			}
		}
	}
	for k := range body {
		if !known[k] {
			errs = append(errs, "unknown field: "+prefix+k)
		}
	}
	return errs
}

// TopLevelFieldNames returns the JSON field names that appear at the top level
// when typ is serialised to JSON. Fields from anonymous (embedded) struct fields
// without a JSON tag are promoted and included, mirroring encoding/json behaviour.
// Used by tests to verify that genSchema and ValidateShape agree on a type's shape.
func TopLevelFieldNames(typ reflect.Type) []string {
	if typ == nil {
		return nil
	}
	t := deref(typ)
	if t.Kind() != reflect.Struct {
		return nil
	}
	var names []string
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Anonymous && f.Tag.Get("json") == "" {
			names = append(names, TopLevelFieldNames(f.Type)...)
		} else if n := JSONTagName(f); n != "" && n != "-" {
			names = append(names, n)
		}
	}
	return names
}
