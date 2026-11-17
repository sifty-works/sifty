package source

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/url"
	"strings"

	"github.com/sifty-works/sifty/internal/fetch"
	"github.com/sifty-works/sifty/internal/listing"
)

type feedSource struct{}

func (s *feedSource) Match(_ *url.URL) bool { return true }

func (s *feedSource) List(ctx context.Context, f fetch.Fetcher, cfg SiteConfig) ([]listing.Posting, error) {
	resp, err := f.Get(ctx, cfg.URL, fetch.Opt{})
	if err != nil {
		return nil, err
	}
	base := resp.URL
	if base == "" {
		base = cfg.URL
	}
	dec := xml.NewDecoder(bytes.NewReader(resp.Body))
	dec.Strict = false
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, ErrNothingRecognised
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "rss":
			var feed rssFeed
			if err := dec.DecodeElement(&feed, &se); err != nil {
				return nil, ErrNothingRecognised
			}
			var out []listing.Posting
			for _, item := range feed.Items {
				text := item.Encoded
				if text == "" {
					text = item.Desc
				}
				title, company := splitCompanyTitle(trim(item.Title), trim(item.Company))
				p := listing.Posting{
					URL:      item.Link,
					Title:    title,
					Company:  company,
					Tags:     item.Categories,
					Text:     text,
					TextKind: listing.HTML,
				}
				if pub, ok := parseDate(item.PubDate); ok {
					p.Published = pub
					p.PublishedBasis = "pubDate"
				}
				if p, ok := finalise(p, base); ok {
					out = append(out, p)
				}
			}
			return out, nil
		case "feed":
			var feed atomFeed
			if err := dec.DecodeElement(&feed, &se); err != nil {
				return nil, ErrNothingRecognised
			}
			var out []listing.Posting
			for _, e := range feed.Entries {
				link := ""
				for _, l := range e.Links {
					if link == "" {
						link = l.Href
					}
					if l.Rel == "alternate" {
						link = l.Href
						break
					}
				}
				text := e.Content
				if text == "" {
					text = e.Summary
				}
				var cats []string
				for _, c := range e.Categories {
					if t := c.Term; t != "" {
						cats = append(cats, t)
					} else if c.Label != "" {
						cats = append(cats, c.Label)
					}
				}
				title, company := splitCompanyTitle(trim(e.Title), trim(e.Company))
				p := listing.Posting{
					URL:      link,
					Title:    title,
					Company:  company,
					Tags:     cats,
					Text:     text,
					TextKind: listing.HTML,
				}
				if pub, ok := parseDate(e.Published); ok {
					p.Published = pub
					p.PublishedBasis = "published"
				}
				if p, ok := finalise(p, base); ok {
					out = append(out, p)
				}
			}
			return out, nil
		default:
			return nil, ErrNothingRecognised
		}
	}
}

// RSS types. The company field tag matches <company> in any namespace,
// including <job:company>. content:encoded uses its standard namespace.

type rssFeed struct {
	Items []rssItem `xml:"channel>item"`
}

type rssItem struct {
	Title      string   `xml:"title"`
	Link       string   `xml:"link"`
	PubDate    string   `xml:"pubDate"`
	Desc       string   `xml:"description"`
	Encoded    string   `xml:"http://purl.org/rss/1.0/modules/content/ encoded"`
	Company    string   `xml:"company"`
	Categories []string `xml:"category"`
}

// Atom types. The updated element is absent so the decoder ignores it.

type atomFeed struct {
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Title      string         `xml:"title"`
	Links      []atomLink     `xml:"link"`
	Published  string         `xml:"published"`
	Content    string         `xml:"content"`
	Summary    string         `xml:"summary"`
	Company    string         `xml:"company"`
	Categories []atomCategory `xml:"category"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
}

type atomCategory struct {
	Term  string `xml:"term,attr"`
	Label string `xml:"label,attr"`
}

// splitCompanyTitle splits "Company: Title" at the first ": " when company is empty.
func splitCompanyTitle(title, company string) (string, string) {
	if company != "" {
		return title, company
	}
	if idx := strings.Index(title, ": "); idx >= 0 {
		return title[idx+2:], title[:idx]
	}
	return title, ""
}
