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

// Attr selects page elements by attribute substring: every element whose named
// attribute value contains Substr, in document order.
type Attr struct{ Attr, Substr string }

// SiteConfig holds the user-configured settings for one source.
// Content, LinkPattern, ListURL and SkipJSONLD are set from the built-in site
// table when the source URL's host matches a known entry; the user's yaml never
// sets these four fields.
type SiteConfig struct {
	Name        string
	URL         string
	Content     []Attr // content block selector(s) for Detail body extraction
	LinkPattern string // regexp for vacancy link paths (generic List)
	ListURL     string // list address read instead of URL (a feed or a page)
	ScriptVar   string // JS variable name holding the jobs array (a16z)
	SkipJSONLD  bool   // skip JSON-LD extraction on this source
}

// mergeConfig returns the site-table entry for the host of s.URL with Name and
// URL copied from s, or s unchanged when no entry exists. A leading "www." is
// stripped before lookup.
func mergeConfig(s SiteConfig) SiteConfig {
	u, _ := url.Parse(s.URL)
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	switch host {
	case "web3.career", "web3.careers":
		return SiteConfig{Name: s.Name, URL: s.URL, SkipJSONLD: true, LinkPattern: `^/[a-z0-9][a-z0-9-]*/\d{5,}$`, Content: []Attr{{Attr: "class", Substr: "text-dark-grey-text"}}}
	case "remote3.co":
		return SiteConfig{Name: s.Name, URL: s.URL, ListURL: "https://remote3.co/api/rss", Content: []Attr{{Attr: "class", Substr: "RemoteJobs_jobDescription"}}}
	case "weworkremotely.com":
		return SiteConfig{Name: s.Name, URL: s.URL, ListURL: "https://weworkremotely.com/remote-jobs.rss"}
	case "cryptocurrencyjobs.co":
		return SiteConfig{Name: s.Name, URL: s.URL, ListURL: "https://cryptocurrencyjobs.co/index.xml"}
	case "jobs.framework.ventures":
		return SiteConfig{Name: s.Name, URL: s.URL, ListURL: "https://jobs.framework.ventures/jobs"}
	case "a16zfrontier.com":
		return SiteConfig{Name: s.Name, URL: s.URL, ScriptVar: "portfolioJobs"}
	}
	return s
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
	srcGeneric    Source = &genericSource{}
)

// Host returns the key that groups sources read one after another.
// Greenhouse, Lever and Ashby boards are requested at a shared API host that
// differs from the board's host; the key is that API host. Any other "jobs."
// host gets an "api." key too, which only groups it with a sibling host.
func Host(u *url.URL) string {
	h := strings.ToLower(u.Host)
	if strings.HasSuffix(h, ".greenhouse.io") {
		return "boards-api." + h[strings.Index(h, ".")+1:]
	}
	if strings.HasPrefix(h, "jobs.") {
		return "api." + h[5:]
	}
	return h
}

// For returns the first Source that matches u, or the generic source for any other address.
func For(u *url.URL) Source {
	for _, s := range []Source{srcGreenhouse, srcLever, srcAshby, srcHN} {
		if s.Match(u) {
			return s
		}
	}
	return srcGeneric
}

// trim returns s with leading and trailing whitespace removed.
func trim(s string) string { return strings.TrimSpace(s) }

// boardSlug returns the first path segment of u (the board or site slug).
func boardSlug(u *url.URL) string {
	p, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSuffix(u.Path, "/"), "/"), "/")
	return p
}

// isRoot reports whether u points to a board root (exactly one path segment).
func isRoot(u *url.URL) bool {
	p := strings.TrimSuffix(u.Path, "/")
	if p == "" || p[0] != '/' {
		return false
	}
	return p[1:] != "" && !strings.Contains(p[1:], "/")
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
	if s = trim(s); s == "" {
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
// href is returned unchanged if it is already absolute, empty, or base is empty.
func resolveURL(href, base string) string {
	if href == "" || base == "" {
		return href
	}
	hURL, err := url.Parse(href)
	if err != nil || hURL.IsAbs() {
		return href
	}
	if bURL, err := url.Parse(base); err == nil {
		return bURL.ResolveReference(hURL).String()
	}
	return href
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
