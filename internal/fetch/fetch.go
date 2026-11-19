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

type Fetcher interface {
	Get(ctx context.Context, url string, opt Opt) (Response, error)
}

type Opt struct {
	Accept string
}

type Response struct {
	URL         string
	Status      int
	ContentType string
	Body        []byte
}

var (
	ErrBlocked   = errors.New("blocked by site")
	ErrNotFound  = errors.New("not found")
	ErrTransient = errors.New("transient error")
	ErrTimeout   = fmt.Errorf("%w: request timed out", ErrTransient)
	ErrRefused   = errors.New("refused: private or disallowed address")
	ErrOffline   = errors.New("offline")
)

func wrapErr(sentinel error, format string, args ...any) error {
	return fmt.Errorf("%w: %s", sentinel, fmt.Sprintf(format, args...))
}

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

type AddressPolicy func(ip net.IP) bool

func PublicOnly(ip net.IP) bool {
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

func domainKey(host string) string {
	host = strings.ToLower(host)
	host = strings.TrimPrefix(host, "www.")
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host
}
