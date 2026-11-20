package source

import (
	"encoding/json"
	"github.com/sifty-works/sifty-server/internal/listing"
	"strings"
	"time"
)

var srcGetro Module = &pageModule{"getro", parseGetro}

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

func parseGetro(body []byte, base string, _ Site) []listing.Posting {
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
