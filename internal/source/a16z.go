package source

import (
	"encoding/json"
	"github.com/sifty-works/sifty-server/internal/listing"
	"strings"
)

var srcA16Z Module = &pageModule{"a16z", parseA16Z}

type a16zCompany struct {
	Jobs []struct {
		Title, URL, CompanyName, CreatedAt string
		Locations                          []string
		Remote                             bool
	}
}

func parseA16Z(body []byte, base string, s Site) []listing.Posting {
	content := findScript(body, s.ScriptVar)
	_, rest, ok := strings.Cut(content, s.ScriptVar)
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
