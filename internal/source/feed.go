package source

import (
	"bytes"
	"encoding/xml"
	"strings"

	"github.com/sifty-works/sifty/internal/listing"
)

// parseFeedXML parses RSS or Atom XML and returns postings.
// Returns ErrNothingRecognised when the content is not RSS or Atom.
func parseFeedXML(body []byte, base string) ([]listing.Posting, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
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
			items := make([]feedItem, 0, len(feed.Items))
			for _, item := range feed.Items {
				text := item.Encoded
				if text == "" {
					text = item.Desc
				}
				title, company := splitCompanyTitle(trim(item.Title), trim(item.Company))
				items = append(items, feedItem{
					url: item.Link, title: title, company: company,
					tags: item.Categories, text: text,
					published: item.PubDate, pubBasis: "pubDate",
				})
			}
			return buildPostings(items, base), nil
		case "feed":
			var feed atomFeed
			if err := dec.DecodeElement(&feed, &se); err != nil {
				return nil, ErrNothingRecognised
			}
			items := make([]feedItem, 0, len(feed.Entries))
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
				items = append(items, feedItem{
					url: link, title: title, company: company,
					tags: cats, text: text,
					published: e.Published, pubBasis: "published",
				})
			}
			return buildPostings(items, base), nil
		default:
			return nil, ErrNothingRecognised
		}
	}
}

// feedItem is an intermediate representation shared by RSS and Atom items.
type feedItem struct {
	url       string
	title     string
	company   string
	tags      []string
	text      string
	published string
	pubBasis  string
}

// buildPostings converts a slice of feedItems into postings, resolving URLs
// against base and running each through finalise.
func buildPostings(items []feedItem, base string) []listing.Posting {
	var out []listing.Posting
	for _, it := range items {
		p := listing.Posting{
			URL:      it.url,
			Title:    it.title,
			Company:  it.company,
			Tags:     it.tags,
			Text:     it.text,
			TextKind: listing.HTML,
		}
		if pub, ok := parseDate(it.published); ok {
			p.Published = pub
			p.PublishedBasis = it.pubBasis
		}
		if p, ok := finalise(p, base); ok {
			out = append(out, p)
		}
	}
	return out
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
