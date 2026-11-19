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

	"github.com/sifty-works/sifty-server/internal/fetch"
	"github.com/sifty-works/sifty-server/internal/listing"
)

// genericSource is the fallback Source: XML feed, JSON-LD, Getro, links.
type genericSource struct{}

func (s *genericSource) Match(_ *url.URL) bool { return true }

func (s *genericSource) List(ctx context.Context, f fetch.Fetcher, cfg SiteConfig) ([]listing.Posting, error) {
	cfg = mergeConfig(cfg)
	fetchURL := cfg.URL
	if cfg.ListURL != "" {
		fetchURL = cfg.ListURL
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
	if cfg.ScriptVar != "" {
		if ps := a16zPostings(resp.Body, base, cfg.ScriptVar); len(ps) > 0 {
			return ps, nil
		}
	}
	if !cfg.SkipJSONLD {
		if ps := jsonldPostings(resp.Body, base, false); len(ps) > 0 {
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
// page is matched by address or unique title; an ambiguous page (multiple
// items share p's title) leaves text and company empty. With SkipJSONLD,
// matched JSON-LD still fills empty fields but its description is ignored and
// body/Content is used for text instead. When company is still absent after
// all JSON-LD attempts, the page <title> is searched for "<role> at <company>".
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
	// tryPageTitle is set when no JSON-LD match was found and the page was not
	// ambiguous, so we can still recover the company from the page <title>.
	tryPageTitle := false
	if cs := jsonldPostings(resp.Body, base, true); len(cs) > 0 {
		if m, ok, ambig := matchJSONLD(cs, p); ok {
			if s.SkipJSONLD {
				m.Text = ""
			}
			p = mergeDetail(p, m)
		} else {
			tryPageTitle = !ambig
		}
	} else {
		useBody = true
		tryPageTitle = true
	}
	if useBody {
		if raw := extractBodyText(resp.Body, s.Content); raw != "" {
			p.Text = listing.Normalize(raw, listing.HTML)
			p.TextKind, p.TextState = listing.HTML, listing.TextFull
		}
	}
	if tryPageTitle && p.Company == "" {
		if co := companyFromPageTitle(resp.Body); co != "" {
			p.Company = co
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
	b := bytes.TrimSpace(bytes.TrimPrefix(body, []byte{0xEF, 0xBB, 0xBF}))
	return strings.Contains(ct, "xml") || strings.Contains(ct, "rss") || strings.Contains(ct, "atom") ||
		bytes.HasPrefix(b, []byte("<?xml")) || bytes.HasPrefix(b, []byte("<rss")) || bytes.HasPrefix(b, []byte("<feed"))
}

// ── JSON-LD ───────────────────────────────────────────────────────────────────

// jsonldPostings parses JSON-LD JobPosting items from body and returns postings
// whose URL can be determined. When keepNoURL is true, items without a url
// field receive pageURL as their address (used by Detail for title matching).
func jsonldPostings(body []byte, pageURL string, keepNoURL bool) []listing.Posting {
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
		if p, ok := jsonldToPosting(item, pageURL, len(items), keepNoURL); ok {
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

func jsonldToPosting(o map[string]any, pageURL string, nItems int, keepNoURL bool) (listing.Posting, bool) {
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
	if postURL == "" && nItems != 1 && !keepNoURL {
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

// matchJSONLD returns the JSON-LD item that uniquely matches p (by address or
// title), ok=true when found. When ok is false and ambiguous is true, more
// than one item shares p's title so the page cannot be attributed to p.
func matchJSONLD(cs []listing.Posting, p listing.Posting) (listing.Posting, bool, bool) {
	if pc, _ := listing.Canonical(p.URL); pc != "" {
		var ms []listing.Posting
		for _, c := range cs {
			if cc, _ := listing.Canonical(c.URL); cc == pc {
				ms = append(ms, c)
			}
		}
		if len(ms) == 1 {
			return ms[0], true, false
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
		return ms[0], true, false
	}
	return listing.Posting{}, false, len(ms) > 1
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

// findScript returns the text content of the first <script> whose markup contains marker.
func findScript(body []byte, marker string) string {
	s := string(body)
	i := strings.Index(s, marker)
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(s[:i+1], "<script")
	if start < 0 {
		return ""
	}
	s = s[start:]
	j := strings.IndexByte(s, '>')
	k := strings.Index(s, "</script>")
	if j < 0 || k < j {
		return ""
	}
	return strings.TrimSpace(s[j+1 : k])
}

func getroPostings(body []byte, base string) []listing.Posting {
	raw := findScript(body, `id="__NEXT_DATA__"`)
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
	if raw == "" || json.Unmarshal([]byte(raw), &nd) != nil {
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
	keyToIdx := map[string]int{} // address → index in out, for company backfill
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
		if !isVacancy {
			return true
		}
		switch strings.ToLower(u.Path[strings.LastIndex(u.Path, "/")+1:]) {
		case "signup", "login", "search", "new", "rss", "feed":
			return true
		}
		if seen[key] {
			// A second link to the same address may carry the company in an h3
			// when the first link only held the title (e.g. listing rows where
			// title is in a first link's h2 and company in a second link's h3).
			if idx, ok := keyToIdx[key]; ok && out[idx].Company == "" {
				if co := firstText(n, func(c *xhtml.Node) bool {
					return c.Type == xhtml.ElementNode && c.Data == "h3"
				}); co != "" {
					out[idx].Company = co
				}
			}
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
			title = firstText(n, func(c *xhtml.Node) bool {
				return c.Type == xhtml.ElementNode && (c.Data == "h2" || c.Data == "h3" || c.Data == "h4")
			})
		}
		if title == "" {
			title = firstText(n, func(c *xhtml.Node) bool {
				return c.Type == xhtml.TextNode && strings.TrimSpace(c.Data) != ""
			})
		}
		lt := strings.ToLower(title)
		if lt == "" || strings.HasPrefix(lt, "apply") || strings.HasPrefix(lt, "view") ||
			strings.HasPrefix(lt, "see all") || strings.HasPrefix(lt, "learn") ||
			strings.HasPrefix(lt, "next") || strings.HasPrefix(lt, "previous") {
			return true
		}
		keyToIdx[key] = len(out)
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

// ── a16z ─────────────────────────────────────────────────────────────────────

type a16zCompany struct {
	Jobs []struct {
		Title, URL, CompanyName, CreatedAt string
		Locations                          []string
		Remote                             bool
	}
}

// a16zPostings reads the script array varName: companies, each with its jobs.
func a16zPostings(body []byte, base, varName string) []listing.Posting {
	content := findScript(body, varName)
	_, rest, ok := strings.Cut(content, varName)
	at := strings.IndexByte(rest, '[')
	var companies []a16zCompany
	if !ok || at < 0 || json.NewDecoder(strings.NewReader(rest[at:])).Decode(&companies) != nil {
		return nil
	}
	var out []listing.Posting
	for _, c := range companies {
		for _, j := range c.Jobs {
			loc := strings.Join(j.Locations, ", ")
			if j.Remote {
				loc = strings.TrimLeft(loc+", Remote", ", ")
			}
			p := listing.Posting{URL: resolveURL(j.URL, base), Title: j.Title, Company: j.CompanyName, Location: loc}
			if t, ok := parseDate(j.CreatedAt); ok {
				p.Published, p.PublishedBasis = t, "createdAt"
			}
			if p, ok := finalise(p, base); ok {
				out = append(out, p)
			}
		}
	}
	return out
}

// ── HTML utilities ────────────────────────────────────────────────────────────

func htmlWalk(n *xhtml.Node, fn func(*xhtml.Node) bool) {
	if n != nil && fn(n) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			htmlWalk(c, fn)
		}
	}
}

// companyFromPageTitle extracts the company name from the page <title> element
// when the title follows the pattern "<role> at <company>" (with an optional
// site suffix after " | " or " - "). Returns "" when the pattern is not found.
func companyFromPageTitle(body []byte) string {
	doc, _ := xhtml.Parse(bytes.NewReader(body))
	var text string
	htmlWalk(doc, func(n *xhtml.Node) bool {
		if text != "" {
			return false
		}
		if n.Type == xhtml.ElementNode && n.Data == "title" && n.FirstChild != nil {
			var sb strings.Builder
			htmlWalk(n, func(c *xhtml.Node) bool {
				if c.Type == xhtml.TextNode {
					sb.WriteString(c.Data)
				}
				return true
			})
			text = strings.Join(strings.Fields(sb.String()), " ")
			return false
		}
		return true
	})
	if text == "" {
		return ""
	}
	// Strip common site suffixes before the " at " split.
	for _, sep := range []string{" | ", " - "} {
		if i := strings.Index(text, sep); i >= 0 {
			text = text[:i]
		}
	}
	text = strings.TrimSpace(text)
	if i := strings.LastIndex(text, " at "); i >= 0 {
		return strings.TrimSpace(text[i+4:])
	}
	return ""
}

// firstText returns the text content of the first descendant of n that
// satisfies pred. Text is gathered by collecting all text-node descendants
// of that node and joining them with a single space.
func firstText(n *xhtml.Node, pred func(*xhtml.Node) bool) string {
	var out string
	htmlWalk(n, func(c *xhtml.Node) bool {
		if out != "" {
			return false
		}
		if !pred(c) {
			return true
		}
		var sb strings.Builder
		htmlWalk(c, func(t *xhtml.Node) bool {
			if t.Type == xhtml.TextNode {
				sb.WriteString(t.Data)
			}
			return true
		})
		out = strings.Join(strings.Fields(sb.String()), " ")
		return false
	})
	return out
}
