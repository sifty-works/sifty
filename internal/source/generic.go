package source

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"

	"github.com/sifty-works/sifty/internal/fetch"
	"github.com/sifty-works/sifty/internal/listing"
)

// genericSource is the fallback Source: XML feed, JSON-LD, Getro, links.
type genericSource struct{}

func (s *genericSource) Match(_ *url.URL) bool { return true }

func (s *genericSource) List(ctx context.Context, f fetch.Fetcher, cfg SiteConfig) ([]listing.Posting, error) {
	cfg = mergeConfig(cfg)
	fetchURL := cfg.URL
	if cfg.FeedURL != "" {
		fetchURL = cfg.FeedURL
	}
	resp, err := f.Get(ctx, fetchURL, fetch.Opt{})
	if err != nil {
		return nil, err
	}
	base := resp.URL
	if base == "" {
		base = fetchURL
	}
	if isXMLContent(resp.ContentType, resp.Body) {
		if ps, err := parseFeedXML(resp.Body, base); err != ErrNothingRecognised {
			return ps, err
		}
	}
	if !cfg.SkipJSONLD {
		if ps := jsonldPostings(resp.Body, base); len(ps) > 0 {
			return ps, nil
		}
	}
	if ps := getroPostings(resp.Body, base); len(ps) > 0 {
		return ps, nil
	}
	if ps := linkPostings(resp.Body, base, cfg.LinkPattern); len(ps) > 0 {
		return ps, nil
	}
	return nil, ErrNothingRecognised
}

// Detail fetches p.URL once and fills text and empty fields. JSON-LD on the
// page is matched by address or unique title+company; ambiguous pages leave
// text empty. With SkipJSONLD, matched JSON-LD still fills empty fields but
// its description is ignored and body/Content is used for text instead.
func Detail(ctx context.Context, f fetch.Fetcher, p listing.Posting, s SiteConfig) (listing.Posting, error) {
	s = mergeConfig(s)
	resp, err := f.Get(ctx, p.URL, fetch.Opt{})
	if err != nil {
		return p, err
	}
	base := resp.URL
	if base == "" {
		base = p.URL
	}
	useBody := s.SkipJSONLD
	if cs := jsonldPostings(resp.Body, base); len(cs) > 0 {
		if m, ok := matchJSONLD(cs, p); ok {
			if s.SkipJSONLD {
				m.Text = ""
			}
			p = mergeDetail(p, m)
		}
	} else {
		useBody = true
	}
	if useBody {
		if raw := extractBodyText(resp.Body, s.Content); raw != "" {
			p.Text = listing.Normalize(raw, listing.HTML)
			p.TextKind, p.TextState = listing.HTML, listing.TextFull
		}
	}
	if p.ApplyURL == "" {
		doc, _ := xhtml.Parse(bytes.NewReader(resp.Body))
		htmlWalk(doc, func(n *xhtml.Node) bool {
			if p.ApplyURL != "" || n.Type != xhtml.ElementNode || n.Data != "a" {
				return p.ApplyURL == ""
			}
			for _, a := range n.Attr {
				if a.Key == "href" {
					if u, ok := listing.ConcreteATS(resolveURL(a.Val, base)); ok {
						p.ApplyURL = u
						return false
					}
				}
			}
			return true
		})
	}
	return p, nil
}

func mergeDetail(p, m listing.Posting) listing.Posting {
	if m.Text != "" {
		p.Text = listing.Normalize(m.Text, listing.HTML)
		p.TextKind, p.TextState = listing.HTML, listing.TextFull
	}
	if p.Company == "Unknown" {
		p.Company = ""
	}
	for _, kv := range [][2]*string{{&p.Company, &m.Company}, {&p.Location, &m.Location}} {
		if *kv[0] == "" {
			*kv[0] = *kv[1]
		}
	}
	if p.Published.IsZero() && !m.Published.IsZero() {
		p.Published, p.PublishedBasis = m.Published, m.PublishedBasis
	}
	if p.Expires.IsZero() && !m.Expires.IsZero() {
		p.Expires = m.Expires
	}
	for k, v := range m.Fields {
		if p.Fields == nil {
			p.Fields = make(map[string]string)
		}
		if p.Fields[k] == "" {
			p.Fields[k] = v
		}
	}
	return p
}

func isXMLContent(ct string, body []byte) bool {
	ct = strings.ToLower(ct)
	if strings.Contains(ct, "xml") || strings.Contains(ct, "rss") || strings.Contains(ct, "atom") {
		return true
	}
	b := bytes.TrimSpace(bytes.TrimPrefix(body, []byte{0xEF, 0xBB, 0xBF}))
	return bytes.HasPrefix(b, []byte("<?xml")) || bytes.HasPrefix(b, []byte("<rss")) || bytes.HasPrefix(b, []byte("<feed"))
}

// ── JSON-LD ───────────────────────────────────────────────────────────────────

func jsonldPostings(body []byte, pageURL string) []listing.Posting {
	doc, _ := xhtml.Parse(bytes.NewReader(body))
	var items []map[string]any
	htmlWalk(doc, func(n *xhtml.Node) bool {
		if n.Type == xhtml.ElementNode && n.Data == "script" {
			for _, a := range n.Attr {
				if strings.ToLower(a.Key) == "type" && strings.ToLower(a.Val) == "application/ld+json" {
					var v any
					if n.FirstChild != nil && json.Unmarshal([]byte(n.FirstChild.Data), &v) == nil {
						collectJobPostings(v, &items)
					}
				}
			}
		}
		return true
	})
	out := make([]listing.Posting, 0, len(items))
	for _, item := range items {
		if p, ok := jsonldToPosting(item, pageURL, len(items)); ok {
			if p, ok = finalise(p, pageURL); ok {
				out = append(out, p)
			}
		}
	}
	return out
}

func collectJobPostings(v any, out *[]map[string]any) {
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			collectJobPostings(e, out)
		}
	case map[string]any:
		for _, e := range ldFlat(t["@type"]) {
			if s, _ := e.(string); s == "JobPosting" {
				*out = append(*out, t)
				return
			}
		}
		for k, val := range t {
			if k != "description" {
				collectJobPostings(val, out)
			}
		}
	}
}

func jsonldToPosting(o map[string]any, pageURL string, nItems int) (listing.Posting, bool) {
	title := ldStr(o, "title")
	if title == "" {
		return listing.Posting{}, false
	}
	var company string
	switch h := o["hiringOrganization"].(type) {
	case string:
		company = h
	case map[string]any:
		company = ldStr(h, "name")
	}
	postURL := resolveURL(ldStr(o, "url"), pageURL)
	if postURL == "" && nItems != 1 {
		return listing.Posting{}, false
	}
	if postURL == "" {
		postURL = pageURL
	}
	var loc []string
	if strings.EqualFold(ldStr(o, "jobLocationType"), "TELECOMMUTE") {
		loc = append(loc, "Remote")
	}
	for _, jl := range ldFlat(o["jobLocation"]) {
		if m, ok := jl.(map[string]any); ok {
			if addr, _ := m["address"].(map[string]any); addr != nil {
				for _, k := range []string{"addressLocality", "addressRegion", "addressCountry"} {
					if s := ldStr(addr, k); s != "" {
						loc = append(loc, s)
					}
				}
			}
		}
	}
	seen := map[string]bool{}
	uniq := loc[:0]
	for _, s := range loc {
		if !seen[s] {
			seen[s] = true
			uniq = append(uniq, s)
		}
	}
	p := listing.Posting{URL: postURL, Title: trim(title), Company: trim(company), Location: strings.Join(uniq, ", ")}
	if desc := ldStr(o, "description"); desc != "" {
		p.Text = desc
		p.TextKind = listing.HTML
	}
	if t, ok := parseDate(ldStr(o, "datePosted")); ok && !t.IsZero() && !t.After(time.Now()) {
		p.Published, p.PublishedBasis = t, "datePosted"
	}
	if t, ok := parseDate(ldStr(o, "validThrough")); ok {
		p.Expires = t
	}
	p.Fields = setFields("employment_type", ldStr(o, "employmentType"), "salary", ldSalaryText(o["baseSalary"]))
	return p, true
}

func matchJSONLD(cs []listing.Posting, p listing.Posting) (listing.Posting, bool) {
	if pc, _ := listing.Canonical(p.URL); pc != "" {
		var ms []listing.Posting
		for _, c := range cs {
			if cc, _ := listing.Canonical(c.URL); cc == pc {
				ms = append(ms, c)
			}
		}
		if len(ms) == 1 {
			return ms[0], true
		}
	}
	pt, pco := strings.ToLower(trim(p.Title)), strings.ToLower(trim(p.Company))
	var ms []listing.Posting
	for _, c := range cs {
		ct, cc := strings.ToLower(trim(c.Title)), strings.ToLower(trim(c.Company))
		if ct == pt && (pco == "" || pco == "unknown" || cc == pco) {
			ms = append(ms, c)
		}
	}
	if len(ms) == 1 {
		return ms[0], true
	}
	return listing.Posting{}, false
}

func ldStr(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func ldFlat(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	if v != nil {
		return []any{v}
	}
	return nil
}
func ldSalaryText(v any) string {
	b, _ := json.Marshal(v)
	s := strings.Trim(string(b), `"`)
	if s != "" && s != "null" {
		return s
	}
	return ""
}

// ── Getro ────────────────────────────────────────────────────────────────────

// ndJob is the job shape inside Getro's __NEXT_DATA__ initialState.jobs.found.
type ndJob struct {
	Title     string   `json:"title"`
	Slug      string   `json:"slug"`
	ApplyURL  string   `json:"url"`
	Locations []string `json:"locations"`
	CreatedAt float64  `json:"createdAt"`
	Org       struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"organization"`
}

// extractNextData returns the text content of <script id="__NEXT_DATA__">.
func extractNextData(body []byte) string {
	s := string(body)
	i := strings.Index(s, `id="__NEXT_DATA__"`)
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(s[i:], '>')
	if j < 0 {
		return ""
	}
	k := strings.Index(s[i+j:], "</script>")
	if k < 0 {
		return ""
	}
	return strings.TrimSpace(s[i+j+1 : i+j+k])
}

func getroPostings(body []byte, base string) []listing.Posting {
	raw := extractNextData(body)
	if raw == "" {
		return nil
	}
	var nd struct {
		Props struct {
			PageProps struct {
				InitialState struct {
					Jobs struct {
						Found []ndJob `json:"found"`
					} `json:"jobs"`
				} `json:"initialState"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if json.Unmarshal([]byte(raw), &nd) != nil {
		return nil
	}
	var out []listing.Posting
	for _, f := range nd.Props.PageProps.InitialState.Jobs.Found {
		if f.Title == "" || f.Org.Name == "" || f.Slug == "" {
			continue
		}
		p := listing.Posting{
			URL:      resolveURL("/companies/"+f.Org.Slug+"/jobs/"+f.Slug, base),
			Title:    f.Title,
			Company:  f.Org.Name,
			Location: strings.Join(f.Locations, ", "),
		}
		p.ApplyURL = resolveURL(f.ApplyURL, base)
		if f.CreatedAt > 0 {
			sec := int64(f.CreatedAt)
			if sec > 1e10 {
				sec /= 1000
			}
			p.Published, p.PublishedBasis = time.Unix(sec, 0).UTC(), "created_at"
		}
		if p, ok := finalise(p, base); ok {
			out = append(out, p)
		}
	}
	return out
}

// ── Link extraction ───────────────────────────────────────────────────────────

var vacancyPathRE = regexp.MustCompile(`(?i)/(?:job|jobs|career|position)/[^/?]+`)

func linkPostings(body []byte, base, pattern string) []listing.Posting {
	var linkRE *regexp.Regexp
	if pattern != "" {
		linkRE, _ = regexp.Compile(pattern)
	}
	doc, _ := xhtml.Parse(bytes.NewReader(body))
	seen := map[string]bool{}
	var out []listing.Posting
	htmlWalk(doc, func(n *xhtml.Node) bool {
		if n.Type != xhtml.ElementNode || n.Data != "a" {
			return true
		}
		var href string
		for _, a := range n.Attr {
			if a.Key == "href" {
				href = a.Val
				break
			}
		}
		abs := resolveURL(href, base)
		if abs == "" {
			return true
		}
		u, err := url.Parse(abs)
		if err != nil {
			return true
		}
		u.RawQuery, u.Fragment = "", ""
		key := u.String()
		isVacancy := linkRE != nil && linkRE.MatchString(u.Path)
		if !isVacancy {
			_, isVacancy = listing.ConcreteATS(u.String())
		}
		if !isVacancy {
			isVacancy = vacancyPathRE.MatchString(u.Path)
		}
		if seen[key] || !isVacancy {
			return true
		}
		switch strings.ToLower(u.Path[strings.LastIndex(u.Path, "/")+1:]) {
		case "signup", "login", "search", "new", "rss", "feed":
			return true
		}
		seen[key] = true
		var title, company string
		for _, a := range n.Attr {
			if a.Key == "aria-label" {
				s := strings.Join(strings.Fields(a.Val), " ")
				if i := strings.Index(s, " – "); i >= 0 {
					company, title = s[:i], s[i+len(" – "):]
				} else {
					title = s
				}
				break
			}
		}
		if title == "" {
			htmlWalk(n, func(c *xhtml.Node) bool {
				if title != "" {
					return false
				}
				if c.Type == xhtml.ElementNode && (c.Data == "h2" || c.Data == "h3" || c.Data == "h4") {
					var sb strings.Builder
					htmlWalk(c, func(t *xhtml.Node) bool {
						if t.Type == xhtml.TextNode {
							sb.WriteString(t.Data)
						}
						return true
					})
					title = strings.Join(strings.Fields(sb.String()), " ")
					return false
				}
				return true
			})
		}
		if title == "" {
			htmlWalk(n, func(c *xhtml.Node) bool {
				if title != "" {
					return false
				}
				if c.Type == xhtml.TextNode {
					if s := strings.TrimSpace(c.Data); s != "" {
						title = s
					}
				}
				return true
			})
		}
		lt := strings.ToLower(title)
		if lt == "" || strings.HasPrefix(lt, "apply") || strings.HasPrefix(lt, "view") ||
			strings.HasPrefix(lt, "see all") || strings.HasPrefix(lt, "learn") ||
			strings.HasPrefix(lt, "next") || strings.HasPrefix(lt, "previous") {
			return true
		}
		out = append(out, listing.Posting{URL: key, Title: title, Company: company, TextState: listing.TextNotFetched})
		return true
	})
	return out
}

// ── Body/content extraction ───────────────────────────────────────────────────

func extractBodyText(body []byte, attrs []Attr) string {
	doc, _ := xhtml.Parse(bytes.NewReader(body))
	inner := func(n *xhtml.Node) string {
		var b strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			_ = xhtml.Render(&b, c)
		}
		return b.String()
	}
	if len(attrs) > 0 {
		var blocks []string
		seen := map[*xhtml.Node]bool{}
		for _, a := range attrs {
			attr := strings.ToLower(a.Attr)
			htmlWalk(doc, func(n *xhtml.Node) bool {
				if n.Type != xhtml.ElementNode || seen[n] {
					return true
				}
				for _, na := range n.Attr {
					if strings.ToLower(na.Key) == attr && strings.Contains(na.Val, a.Substr) {
						seen[n] = true
						blocks = append(blocks, inner(n))
					}
				}
				return true
			})
		}
		if len(blocks) > 0 {
			return strings.Join(blocks, "\n")
		}
	}
	for _, tag := range []string{"main", "article", "body"} {
		var found *xhtml.Node
		htmlWalk(doc, func(n *xhtml.Node) bool {
			if found == nil && n.Type == xhtml.ElementNode && n.Data == tag {
				found = n
				return false
			}
			return found == nil
		})
		if found != nil {
			return inner(found)
		}
	}
	return ""
}

// ── HTML utilities ────────────────────────────────────────────────────────────

func htmlWalk(n *xhtml.Node, fn func(*xhtml.Node) bool) {
	if n != nil && fn(n) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			htmlWalk(c, fn)
		}
	}
}
