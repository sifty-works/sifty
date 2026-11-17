package source

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"

	"github.com/sifty-works/sifty/internal/fetch"
	"github.com/sifty-works/sifty/internal/listing"
)

type leverSource struct{}

func (s *leverSource) Match(u *url.URL) bool {
	switch strings.ToLower(u.Host) {
	case "jobs.lever.co", "jobs.eu.lever.co":
		return isRoot(u)
	}
	return false
}

func (s *leverSource) List(ctx context.Context, f fetch.Fetcher, cfg SiteConfig) ([]listing.Posting, error) {
	boardURL, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, ErrNothingRecognised
	}
	site := boardSlug(boardURL)
	apiURL := "https://" + Host(boardURL) + "/v0/postings/" + site + "?mode=json"
	resp, err := f.Get(ctx, apiURL, fetch.Opt{})
	if err != nil {
		return nil, err
	}
	var body []lvPosting
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		return nil, ErrNothingRecognised
	}
	company := trim(cfg.Name)
	if company == "" {
		company = site
	}
	var postings []listing.Posting
	for _, post := range body {
		// Build text: description + lists as heading+list + additional.
		var tb strings.Builder
		tb.WriteString(post.Description)
		for _, lst := range post.Lists {
			if t := trim(lst.Text); t != "" {
				tb.WriteString("<h3>" + html.EscapeString(t) + "</h3>")
			}
			if c := trim(lst.Content); c != "" {
				tb.WriteString("<ul>" + c + "</ul>")
			}
		}
		tb.WriteString(post.Additional)
		// Build salary field value.
		var salary string
		if sr := post.SalaryRange; sr != nil && (sr.Min != 0 || sr.Max != 0) {
			if sr.Max > 0 {
				salary = fmt.Sprintf("%d–%d", sr.Min, sr.Max)
			} else {
				salary = fmt.Sprintf("%d", sr.Min)
			}
			if c := trim(sr.Currency); c != "" {
				salary += " " + c
			}
			if i := trim(sr.Interval); i != "" {
				salary += " " + i
			}
		}
		p := listing.Posting{
			URL:      post.HostedURL,
			ApplyURL: post.HostedURL,
			Title:    post.Text,
			Company:  company,
			Location: post.Categories.Location,
			Tags:     []string{post.Categories.Team, post.Categories.Department},
			Fields:   setFields("employment_type", post.Categories.Commitment, "workplace_type", post.WorkplaceType, "salary", salary),
			Text:     tb.String(),
			TextKind: listing.HTML,
		}
		if post.CreatedAt > 0 {
			p.Published = time.UnixMilli(post.CreatedAt).UTC()
			p.PublishedBasis = "createdAt"
		}
		if p, ok := finalise(p, ""); ok {
			postings = append(postings, p)
		}
	}
	return postings, nil
}

type lvPosting struct {
	Text          string       `json:"text"`
	HostedURL     string       `json:"hostedUrl"`
	CreatedAt     int64        `json:"createdAt"`
	Categories    lvCategories `json:"categories"`
	WorkplaceType string       `json:"workplaceType"`
	SalaryRange   *lvSalary    `json:"salaryRange"`
	Description   string       `json:"description"`
	Lists         []lvList     `json:"lists"`
	Additional    string       `json:"additional"`
}

type lvCategories struct {
	Location   string `json:"location"`
	Team       string `json:"team"`
	Department string `json:"department"`
	Commitment string `json:"commitment"`
}

type lvSalary struct {
	Min      int    `json:"min"`
	Max      int    `json:"max"`
	Currency string `json:"currency"`
	Interval string `json:"interval"`
}

type lvList struct {
	Text    string `json:"text"`
	Content string `json:"content"`
}
