// Package fetch is the only way the application requests job sites,
// vacancy pages, employers' pages and link documents.
package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Fetcher retrieves a URL and returns the response. Errors are one of
// ErrBlocked, ErrNotFound, ErrTransient, ErrRefused or ErrOffline.
type Fetcher interface {
	Get(ctx context.Context, url string, opt Opt) (Response, error)
}

// Opt carries per-request options.
type Opt struct {
	Accept string
}

// Response is a successful HTTP response.
// URL is the final address after redirects.
type Response struct {
	URL         string
	Status      int
	ContentType string
	Body        []byte
}

// Sentinel errors returned by Fetcher.Get.
var (
	ErrBlocked   = errors.New("blocked by site")
	ErrNotFound  = errors.New("not found")
	ErrTransient = errors.New("transient error")
	// ErrTimeout wraps ErrTransient and is returned when a per-request
	// deadline fires.  errors.Is(ErrTimeout, ErrTransient) is true.
	ErrTimeout = fmt.Errorf("%w: request timed out", ErrTransient)
	ErrRefused = errors.New("refused: private or disallowed address")
	ErrOffline = errors.New("offline")
)

func wrapErr(sentinel error, format string, args ...any) error {
	return fmt.Errorf("%w: %s", sentinel, fmt.Sprintf(format, args...))
}

// UserText returns a short phrase for the user describing err.
func UserText(err error) string {
	switch {
	case errors.Is(err, ErrBlocked):
		return "Blocked by site"
	case errors.Is(err, ErrNotFound):
		return "Not found"
	case errors.Is(err, ErrTimeout):
		return "Timed out"
	case errors.Is(err, ErrTransient):
		return "Site not answering"
	case errors.Is(err, ErrRefused):
		return "Refused"
	case errors.Is(err, ErrOffline):
		return "Offline"
	default:
		return "Error"
	}
}

// StatusError is the single rule for reading an HTTP response.
// It returns nil for a 2xx answer, ErrBlocked for a 403 or challenge page,
// ErrNotFound for 404 or 410, and ErrTransient for every other non-2xx status.
// A nil header is safe.
func StatusError(status int, header http.Header, body []byte) error {
	cf := header.Get("cf-mitigated") == "challenge" ||
		containsTitleExact(body, "Just a moment...") ||
		(status == 503 && bytes.Contains(body, []byte("challenge-platform")))
	switch {
	case status == 403 || cf:
		return ErrBlocked
	case status == 404 || status == 410:
		return ErrNotFound
	case status >= 200 && status < 300:
		return nil
	default:
		return ErrTransient
	}
}

// containsTitleExact reports whether the HTML body has a <title> exactly
// equal to title (case-sensitive, whitespace-trimmed).
func containsTitleExact(body []byte, title string) bool {
	lower := bytes.ToLower(body)
	start := bytes.Index(lower, []byte("<title>"))
	if start < 0 {
		return false
	}
	start += len("<title>")
	end := bytes.Index(lower[start:], []byte("</title>"))
	if end < 0 {
		return false
	}
	return strings.TrimSpace(string(body[start:start+end])) == title
}

// AddressPolicy decides whether a resolved or dialled IP may be used.
type AddressPolicy func(ip net.IP) bool

// PublicOnly refuses private, loopback, link-local, multicast and
// unspecified addresses (IPv4 and IPv6, including IPv4-mapped IPv6).
func PublicOnly(ip net.IP) bool {
	// Normalise IPv4-mapped IPv6 to plain IPv4 so IsPrivate works correctly.
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	return !ip.IsLoopback() &&
		!ip.IsPrivate() &&
		!ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() &&
		!ip.IsMulticast() &&
		!ip.IsUnspecified()
}

// domainKey returns the lowercased host without a leading "www.".
func domainKey(host string) string {
	host = strings.ToLower(host)
	host = strings.TrimPrefix(host, "www.")
	// Drop port if present.
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host
}
