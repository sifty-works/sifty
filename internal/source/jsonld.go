package source

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sifty-works/sifty-server/internal/listing"
	xhtml "golang.org/x/net/html"
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
		for _, alr := range ldFlat(o["applicantLocationRequirements"]) {
			if m, ok := alr.(map[string]any); ok {
				if s := ldStr(m, "name"); s != "" {
					loc = append(loc, s)
				}
			}
		}
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
	p := listing.Posting{URL: postURL, Title: trim(title), Company: trim(company), Location: strings.Join(uniq, " · ")}
	if desc := ldStr(o, "description"); desc != "" {
		p.Text = desc
	}
	if t, ok := parseDate(ldStr(o, "datePosted")); ok {
		p.Published, p.PublishedBasis = t, "datePosted"
	}
	if t, ok := parseDate(ldStr(o, "validThrough")); ok {
		p.Expires = t
	}
	p.Fields = setFields("employment_type", ldStrFirst(o["employmentType"]), "salary", ldSalaryText(o["baseSalary"]))
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
	if len(ms) == 0 && len(cs) == 1 {
		ct := strings.ToLower(removePunct(trim(cs[0].Title)))
		if ct != "" && ct == strings.ToLower(removePunct(trim(p.Title))) {
			return cs[0], true, false
		}
	}
	return listing.Posting{}, false, len(ms) > 1
}

func ldStr(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func ldStrFirst(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		if len(t) > 0 {
			s, _ := t[0].(string)
			return s
		}
	}
	return ""
}

func ldFloat(m map[string]any, k string) float64 {
	n, _ := m[k].(float64)
	return n
}

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
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	cur := ldStr(m, "currency")
	qv, ok := m["value"].(map[string]any)
	if !ok {
		return ""
	}
	unit := strings.ToLower(ldStr(qv, "unitText"))
	per := map[string]string{"year": "/year", "month": "/month", "hour": "/hour", "day": "/day"}[unit]
	lo, hi, single := ldFloat(qv, "minValue"), ldFloat(qv, "maxValue"), ldFloat(qv, "value")
	var num string
	switch {
	case lo > 0 && hi > 0:
		num = fmt.Sprintf("%.0f–%.0f", lo, hi)
	case single > 0:
		num = fmt.Sprintf("%.0f", single)
	case hi > 0:
		num = fmt.Sprintf("%.0f", hi)
	default:
		return ""
	}
	var parts []string
	if cur != "" {
		parts = append(parts, cur)
	}
	parts = append(parts, num)
	return strings.Join(parts, " ") + per
}

func removePunct(s string) string {
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == ' ' {
			return r
		}
		return ' '
	}, s)
	return strings.Join(strings.Fields(s), " ")
}
