// Package apperr defines typed errors that map to HTTP status codes.
// Services and repos import and return these; the server maps them to
// response status codes without letting Go error text reach the user.
package apperr

// Invalid signals that the request input is invalid (HTTP 422).
type Invalid struct{ Message string }

func (e Invalid) Error() string { return e.Message }

// NotFound signals that the requested resource does not exist (HTTP 404).
type NotFound struct{ Message string }

func (e NotFound) Error() string { return e.Message }

// Conflict signals a state conflict (HTTP 409).
type Conflict struct{ Message string }

func (e Conflict) Error() string { return e.Message }

// Busy signals a temporary lock contention (HTTP 423).
type Busy struct{ Message string }

func (e Busy) Error() string { return e.Message }
