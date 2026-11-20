package source

import (
	"bytes"
	"encoding/json"
	"github.com/sifty-works/sifty-server/internal/listing"
	xhtml "golang.org/x/net/html"
	"strings"
)

func parseJSONLD(body []byte, pageURL string, keepNoURL bool) []listing.Posting {
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
	}
	if t, ok := parseDate(ldStr(o, "datePosted")); ok {
		p.Published, p.PublishedBasis = t, "datePosted"
	}
	if t, ok := parseDate(ldStr(o, "validThrough")); ok {
		p.Expires = t
	}
	p.Fields = setFields("employment_type", ldStr(o, "employmentType"), "salary", ldSalaryText(o["baseSalary"]))
	return p, true
}

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
