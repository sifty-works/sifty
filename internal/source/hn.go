package source

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/sifty-works/sifty-server/internal/fetch"
	"github.com/sifty-works/sifty-server/internal/listing"
)

var srcHN Source = &hnSource{}

type hnSource struct{}

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

func (s *hnSource) List(ctx context.Context, f fetch.Fetcher, cfg SiteConfig) ([]listing.Posting, error) {
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
			if p, ok = finalise(p, cfg.URL); ok {
				out = append(out, p)
			}
		}
	}
	return out, nil
}

const hnWorkTypeStr = " remote onsite hybrid full-time part-time contract intern "

func hnCommentToPosting(c *hnComment) (listing.Posting, bool) {
	firstLine, _, _ := strings.Cut(c.Text, "<p>")
	firstLine = listing.PlainText(firstLine)
	parts := strings.Split(firstLine, "|")
	company := strings.TrimSpace(parts[0])
	if len(parts) < 2 || company == "" {
		return listing.Posting{}, false
	}
	var title, loc string
	for _, pt := range parts[1:] {
		pt = strings.TrimSpace(pt)
		low := strings.ToLower(pt)
		if strings.Contains(hnWorkTypeStr, " "+low+" ") || strings.ContainsAny(pt, "$£€") || strings.HasPrefix(low, "http") ||
			(strings.Contains(pt, ".") && !strings.Contains(pt, " ")) {
			continue
		}
		if title == "" {
			title = pt
			continue
		}
		if loc == "" && pt != title {
			loc = pt
		}
	}
	if title == "" {
		return listing.Posting{}, false
	}
	p := listing.Posting{
		URL:     fmt.Sprintf("https://news.ycombinator.com/item?id=%d", c.ID),
		Company: company, Title: title, Location: loc,
		Text: c.Text,
	}
	if t, ok := parseDate(c.CreatedAt); ok {
		p.Published, p.PublishedBasis = t, "created_at"
	}
	return p, true
}
