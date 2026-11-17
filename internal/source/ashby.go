package source

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/sifty-works/sifty/internal/fetch"
	"github.com/sifty-works/sifty/internal/listing"
)

type ashbySource struct{}

func (s *ashbySource) Match(u *url.URL) bool {
	return strings.ToLower(u.Host) == "jobs.ashbyhq.com" && isRoot(u)
}

func (s *ashbySource) List(ctx context.Context, f fetch.Fetcher, cfg SiteConfig) ([]listing.Posting, error) {
	boardURL, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, ErrNothingRecognised
	}
	apiURL := fmt.Sprintf("https://api.ashbyhq.com/posting-api/job-board/%s?includeCompensation=true", boardSlug(boardURL))
	resp, err := f.Get(ctx, apiURL, fetch.Opt{})
	if err != nil {
		return nil, err
	}
	var body ashbyResponse
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		return nil, ErrNothingRecognised
	}
	company := trim(cfg.Name)
	if company == "" {
		company = trim(body.Organization.Name)
	}
	var postings []listing.Posting
	for _, job := range body.Jobs {
		if job.IsListed != nil && !*job.IsListed {
			continue
		}
		loc := trim(job.Location)
		if job.IsRemote && !strings.Contains(strings.ToLower(loc), "remote") {
			if loc != "" {
				loc += ", Remote"
			} else {
				loc = "Remote"
			}
		}
		applyURL := trim(job.ApplyURL)
		if applyURL == "" {
			applyURL = trim(job.JobURL)
		}
		var salary string
		if job.Compensation != nil {
			salary = trim(job.Compensation.TierSummary)
		}
		p := listing.Posting{
			URL:      trim(job.JobURL),
			ApplyURL: applyURL,
			Title:    trim(job.Title),
			Company:  company,
			Location: loc,
			Tags:     []string{job.Department, job.Team},
			Fields:   setFields("employment_type", job.EmploymentType, "salary", salary),
			Text:     job.DescriptionHTML,
			TextKind: listing.HTML,
		}
		if pub, ok := parseDate(job.PublishedAt); ok {
			p.Published = pub
			p.PublishedBasis = "publishedAt"
		}
		if p, ok := finalise(p, ""); ok {
			postings = append(postings, p)
		}
	}
	return postings, nil
}

type ashbyResponse struct {
	Organization struct {
		Name string `json:"name"`
	} `json:"organization"`
	Jobs []ashbyJob `json:"jobs"`
}

type ashbyJob struct {
	ID              string     `json:"id"`
	Title           string     `json:"title"`
	JobURL          string     `json:"jobUrl"`
	ApplyURL        string     `json:"applyUrl"`
	Location        string     `json:"location"`
	IsRemote        bool       `json:"isRemote"`
	IsListed        *bool      `json:"isListed"`
	Department      string     `json:"department"`
	Team            string     `json:"team"`
	EmploymentType  string     `json:"employmentType"`
	PublishedAt     string     `json:"publishedAt"`
	DescriptionHTML string     `json:"descriptionHtml"`
	Compensation    *ashbyComp `json:"compensation"`
}

type ashbyComp struct {
	TierSummary string `json:"compensationTierSummary"`
}
