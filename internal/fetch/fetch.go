// Package fetch is the only way the application requests job sites,
// vacancy pages, employers' pages and link documents.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"net"
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
	ErrRefused   = errors.New("refused: private or disallowed address")
	ErrOffline   = errors.New("offline")
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
	case errors.Is(err, ErrTransient):
		return "Timed out"
	case errors.Is(err, ErrRefused):
		return "Refused"
	case errors.Is(err, ErrOffline):
		return "Offline"
	default:
		return "Error"
	}
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
