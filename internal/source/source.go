// Package source turns a source address into postings through one fetch.Fetcher.
package source

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/sifty-works/sifty/internal/fetch"
	"github.com/sifty-works/sifty/internal/listing"
)

// ErrNothingRecognised is returned when a feed or page is not in a recognised format.
// User text: "Nothing recognised on the page".
var ErrNothingRecognised = errors.New("nothing recognised on the page")

// SiteConfig holds the user-configured settings for one source.
type SiteConfig struct {
	Name string
	URL  string
}

// Source reads postings from one kind of job site.
type Source interface {
	Match(u *url.URL) bool
	List(ctx context.Context, f fetch.Fetcher, s SiteConfig) ([]listing.Posting, error)
}

var (
	srcGreenhouse Source = &greenhouseSource{}
	srcLever      Source = &leverSource{}
	srcAshby      Source = &ashbySource{}
	srcFeed       Source = &feedSource{}
)

// Host returns the API host used when requesting u's board.
// Greenhouse, Lever and Ashby boards make requests to a shared API host that
// differs from the board's URL host. Every other source uses its own host.
func Host(u *url.URL) string {
	switch strings.ToLower(u.Host) {
	case "boards.greenhouse.io", "job-boards.greenhouse.io":
		return "boards-api.greenhouse.io"
	case "boards.eu.greenhouse.io", "job-boards.eu.greenhouse.io":
		return "boards-api.eu.greenhouse.io"
	case "jobs.lever.co":
		return "api.lever.co"
	case "jobs.eu.lever.co":
		return "api.eu.lever.co"
	case "jobs.ashbyhq.com":
		return "api.ashbyhq.com"
	default:
		return strings.ToLower(u.Host)
	}
}

// For returns the first Source that matches u, or the feed source for any other address.
func For(u *url.URL) Source {
	for _, s := range []Source{srcGreenhouse, srcLever, srcAshby} {
		if s.Match(u) {
			return s
		}
	}
	return srcFeed
}

// trim returns s with leading and trailing whitespace removed.
func trim(s string) string { return strings.TrimSpace(s) }

// boardSlug returns the first path segment of u (the board or site slug).
func boardSlug(u *url.URL) string {
	p := strings.TrimPrefix(strings.TrimSuffix(u.Path, "/"), "/")
	if idx := strings.Index(p, "/"); idx >= 0 {
		return p[:idx]
	}
	return p
}

// isRoot reports whether u points to a board root (exactly one path segment).
func isRoot(u *url.URL) bool {
	p := strings.TrimSuffix(u.Path, "/")
	if p == "" || p[0] != '/' {
		return false
	}
	inner := p[1:] // strip leading /
	return inner != "" && !strings.Contains(inner, "/")
}

// dateFormats tried in order by parseDate.
var dateFormats = []string{
	time.RFC3339Nano,
	time.RFC3339,
	time.RFC1123Z,
	time.RFC1123,
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 2006 15:04:05 MST",
	"2006-01-02T15:04:05",
	"2006-01-02",
}

// parseDate tries to parse s using common date formats.
// Returns the zero Time and false when s cannot be parsed.
func parseDate(s string) (time.Time, bool) {
	s = trim(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, f := range dateFormats {
		if t, err := time.Parse(f, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// resolveURL resolves href against base, returning an absolute URL.
// href is returned unchanged if it is already absolute or base is empty.
func resolveURL(href, base string) string {
	if href == "" {
		return ""
	}
	hURL, err := url.Parse(href)
	if err != nil || hURL.IsAbs() {
		return href
	}
	if base == "" {
		return href
	}
	bURL, err := url.Parse(base)
	if err != nil {
		return href
	}
	return bURL.ResolveReference(hURL).String()
}

// setFields returns a map with the given key/value pairs, omitting empty values.
// kvs must be alternating key, value strings.
func setFields(kvs ...string) map[string]string {
	var m map[string]string
	for i := 0; i+1 < len(kvs); i += 2 {
		if v := trim(kvs[i+1]); v != "" {
			if m == nil {
				m = make(map[string]string)
			}
			m[kvs[i]] = v
		}
	}
	return m
}

// finalise trims all string fields of p, resolves URL and ApplyURL against base,
// drops empty tags, sets TextState from whether text is present, and reports
// whether the posting is kept. A posting is dropped when title or URL is empty.
func finalise(p listing.Posting, base string) (listing.Posting, bool) {
	p.URL = resolveURL(trim(p.URL), base)
	p.ApplyURL = resolveURL(trim(p.ApplyURL), base)
	p.Title = trim(p.Title)
	p.Company = trim(p.Company)
	p.Location = trim(p.Location)
	p.Text = trim(p.Text)
	var tags []string
	for _, t := range p.Tags {
		if t = trim(t); t != "" {
			tags = append(tags, t)
		}
	}
	p.Tags = tags
	if p.Text != "" {
		p.TextState = listing.TextFull
	} else {
		p.TextState = listing.TextNotFetched
	}
	return p, p.Title != "" && p.URL != ""
}
