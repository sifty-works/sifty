package source

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/sifty-works/sifty-server/internal/fetch"
	"github.com/sifty-works/sifty-server/internal/listing"
)

var srcHN Module = &hnSource{}

type hnSource struct{}

func (s *hnSource) Name() string { return "hn" }
func (s *hnSource) Match(u *url.URL) bool {
	return strings.ToLower(u.Host) == "news.ycombinator.com"
}

type hnSearchHit struct{ ObjectID, Title string }
type hnSearchResult struct{ Hits []hnSearchHit }
type hnComment struct {
	ID        int64
	Text      string
	CreatedAt string `json:"created_at"`
	Children  []*hnComment
}

func (s *hnSource) List(ctx context.Context, f fetch.Fetcher, cfg Site) ([]listing.Posting, error) {
	var sr hnSearchResult
	if err := fetchJSON(ctx, f, "https://hn.algolia.com/api/v1/search_by_date?tags=story,author_whoishiring&hitsPerPage=5", &sr); err != nil {
		return nil, err
	}
	var storyID string
	for _, h := range sr.Hits {
		if strings.HasPrefix(h.Title, "Ask HN: Who is hiring?") {
			storyID = h.ObjectID
			break
		}
	}
	if storyID == "" {
		return nil, ErrNothingRecognised
	}
	var item hnComment
	if err := fetchJSON(ctx, f, "https://hn.algolia.com/api/v1/items/"+storyID, &item); err != nil {
		return nil, err
	}
	var out []listing.Posting
	for _, c := range item.Children {
		if p, ok := hnCommentToPosting(c); ok {
			if p, ok = finalise(p, cfg.Address); ok {
				out = append(out, p)
			}
		}
	}
	return out, nil
}

func hnCommentToPosting(c *hnComment) (listing.Posting, bool) {
	firstLine, _, _ := strings.Cut(c.Text, "<p>")
	firstLine = listing.PlainText(firstLine)
	parts := strings.Split(firstLine, "|")
	company := strings.TrimSpace(parts[0])
	if len(parts) < 2 || company == "" {
		return listing.Posting{}, false
	}
	var title, loc, workType, employType, salary string
	for _, pt := range parts[1:] {
		pt = strings.TrimSpace(pt)
		low := strings.ToLower(pt)
		if strings.HasPrefix(low, "http") || (strings.Contains(pt, ".") && !strings.Contains(pt, " ")) {
			continue
		}
		if strings.ContainsAny(pt, "$£€") {
			if salary == "" {
				salary = pt
			}
			continue
		}
		prefix, inner := hnParenthetical(pt)
		prefixLow := strings.ToLower(prefix)
		if prefixLow == "remote" || prefixLow == "onsite" || prefixLow == "on-site" || prefixLow == "hybrid" {
			if workType == "" {
				workType = prefix
			}
			if inner != "" && loc == "" {
				loc = inner
			}
			continue
		}
		if low == "full-time" || low == "full time" || low == "part-time" || low == "part time" ||
			low == "fulltime" || low == "parttime" ||
			low == "contract" || low == "freelance" || low == "intern" || low == "temporary" {
			if employType == "" {
				employType = pt
			}
			continue
		}
		if title == "" {
			title = pt
			continue
		}
		if loc == "" {
			loc = pt
		}
	}
	if title == "" && len(parts) >= 2 {
		title = strings.TrimSpace(parts[1])
	}
	if title == "" {
		return listing.Posting{}, false
	}
	p := listing.Posting{
		URL:     fmt.Sprintf("https://news.ycombinator.com/item?id=%d", c.ID),
		Company: company, Title: title, Location: loc,
		Text:   c.Text,
		Fields: setFields("workplace_type", workType, "employment_type", employType, "salary", salary),
	}
	if t, ok := parseDate(c.CreatedAt); ok {
		p.Published, p.PublishedBasis = t, "created_at"
	}
	return p, true
}

func hnParenthetical(s string) (string, string) {
	i := strings.Index(s, "(")
	j := strings.Index(s, ")")
	if i > 0 && j > i {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1 : j])
	}
	return s, ""
}
