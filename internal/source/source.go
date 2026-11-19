package source

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/sifty-works/sifty-server/internal/fetch"
	"github.com/sifty-works/sifty-server/internal/listing"
)

var ErrNothingRecognised = errors.New("nothing recognised on the page")

func fetchJSON(ctx context.Context, f fetch.Fetcher, apiURL string, dst any) error {
	resp, err := f.Get(ctx, apiURL, fetch.Opt{})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(resp.Body, dst); err != nil {
		return ErrNothingRecognised
	}
	return nil
}

type Attr struct{ Attr, Substr string }

type SiteConfig struct {
	Name        string
	URL         string
	Content     []Attr
	LinkPattern string
	ListURL     string
	ScriptVar   string
	SkipJSONLD  bool
}

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

func For(u *url.URL) Source {
	for _, s := range []Source{srcGreenhouse, srcLever, srcAshby, srcHN} {
		if s.Match(u) {
			return s
		}
	}
	return srcGeneric
}

func trim(s string) string { return strings.TrimSpace(s) }

func boardSlug(u *url.URL) string {
	p, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSuffix(u.Path, "/"), "/"), "/")
	return p
}

func isRoot(u *url.URL) bool {
	p := strings.TrimSuffix(u.Path, "/")
	if p == "" || p[0] != '/' {
		return false
	}
	return p[1:] != "" && !strings.Contains(p[1:], "/")
}

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
