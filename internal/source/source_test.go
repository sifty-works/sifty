package source_test

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sifty-works/sifty-server/internal/fetch"
	"github.com/sifty-works/sifty-server/internal/fetch/fetchtest"
	"github.com/sifty-works/sifty-server/internal/listing"
	"github.com/sifty-works/sifty-server/internal/source"
)

func TestFor_routing(t *testing.T) {
	cases := []struct {
		name   string
		addr   string
		apiURL string
		body   string
	}{
		{"Greenhouse board", "https://boards.greenhouse.io/acme", "https://boards-api.greenhouse.io/v1/boards/acme/jobs?content=true", `{"company":{"name":"Acme"},"jobs":[]}`},
		{"Greenhouse board trailing slash", "https://boards.greenhouse.io/acme/", "https://boards-api.greenhouse.io/v1/boards/acme/jobs?content=true", `{"company":{"name":"Acme"},"jobs":[]}`},
		{"Greenhouse job-boards", "https://job-boards.greenhouse.io/acme", "https://boards-api.greenhouse.io/v1/boards/acme/jobs?content=true", `{"company":{"name":"Acme"},"jobs":[]}`},
		{"Greenhouse EU", "https://boards.eu.greenhouse.io/acme", "https://boards-api.eu.greenhouse.io/v1/boards/acme/jobs?content=true", `{"company":{"name":"Acme"},"jobs":[]}`},
		{"Greenhouse job-boards EU", "https://job-boards.eu.greenhouse.io/acme", "https://boards-api.eu.greenhouse.io/v1/boards/acme/jobs?content=true", `{"company":{"name":"Acme"},"jobs":[]}`},
		{"Greenhouse vacancy page falls to feed", "https://boards.greenhouse.io/acme/jobs/1234", "https://boards.greenhouse.io/acme/jobs/1234", "<html></html>"},
		{"Lever board", "https://jobs.lever.co/techstartup", "https://api.lever.co/v0/postings/techstartup?mode=json", "[]"},
		{"Lever EU", "https://jobs.eu.lever.co/techstartup", "https://api.eu.lever.co/v0/postings/techstartup?mode=json", "[]"},
		{"Ashby board", "https://jobs.ashbyhq.com/nexgen", "https://api.ashbyhq.com/posting-api/job-board/nexgen?includeCompensation=true", `{"organization":{"name":""},"jobs":[]}`},
		{"other URL falls to feed", "https://weworkremotely.com/remote-jobs.rss", "https://weworkremotely.com/remote-jobs.rss", `<rss version="2.0"><channel></channel></rss>`},
		{"arbitrary URL falls to feed", "https://example.com/jobs/feed.xml", "https://example.com/jobs/feed.xml", `<rss version="2.0"><channel></channel></rss>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := fetchtest.NewMap()
			m.Set(tc.apiURL, fetchtest.MapEntry{
				Response: fetch.Response{URL: tc.apiURL, Status: 200, Body: []byte(tc.body)},
			})
			cfg := source.SiteConfig{URL: tc.addr}
			source.For(mustParseURL(t, tc.addr)).List(context.Background(), m, cfg)
			reqs := m.Requests()
			if len(reqs) != 1 || reqs[0] != tc.apiURL {
				t.Errorf("requested %v, want [%s]", reqs, tc.apiURL)
			}
		})
	}
}

func TestList_unrecognised_format(t *testing.T) {
	cases := []struct {
		name   string
		addr   string
		apiURL string
		body   string
	}{
		{"feed HTML page", "http://example.com/", "http://example.com/", "<!DOCTYPE html><html><body>Jobs</body></html>"},
		{"Greenhouse invalid JSON", "https://boards.greenhouse.io/acme", "https://boards-api.greenhouse.io/v1/boards/acme/jobs?content=true", "<!doctype html>"},
		{"Lever invalid JSON", "https://jobs.lever.co/acme", "https://api.lever.co/v0/postings/acme?mode=json", "{not json}"},
		{"Ashby invalid JSON", "https://jobs.ashbyhq.com/acme", "https://api.ashbyhq.com/posting-api/job-board/acme?includeCompensation=true", "not json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := fetchtest.NewMap()
			m.Set(tc.apiURL, fetchtest.MapEntry{
				Response: fetch.Response{URL: tc.apiURL, Status: 200, Body: []byte(tc.body)},
			})
			cfg := source.SiteConfig{URL: tc.addr}
			_, err := source.For(mustParseURL(t, tc.addr)).List(context.Background(), m, cfg)
			if err != source.ErrNothingRecognised {
				t.Errorf("error = %v, want ErrNothingRecognised", err)
			}
		})
	}
}

func TestList_fetcher_error_returned(t *testing.T) {
	m := fetchtest.NewMap()
	m.Set("http://blocked.example.com/feed", fetchtest.MapEntry{Err: fetch.ErrBlocked})
	cfg := source.SiteConfig{URL: "http://blocked.example.com/feed"}
	_, err := source.For(mustParseURL(t, cfg.URL)).List(context.Background(), m, cfg)
	if err != fetch.ErrBlocked {
		t.Errorf("error = %v, want ErrBlocked", err)
	}
}

func TestFeed_RSS_maps_all_fields(t *testing.T) {
	body := mustReadFile(t, "testdata/rss.xml")
	feedURL := "http://feeds.acme.example.com/jobs"
	m := fetchtest.NewMap()
	m.Set(feedURL, fetchtest.MapEntry{
		Response: fetch.Response{URL: feedURL, Status: 200, ContentType: "application/rss+xml", Body: body},
	})
	cfg := source.SiteConfig{Name: "Acme Jobs", URL: feedURL}
	postings, err := source.For(mustParseURL(t, feedURL)).List(context.Background(), m, cfg)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(postings) != 2 {
		t.Fatalf("got %d postings, want 2", len(postings))
	}

	p := postings[0]
	if p.Title != "Senior Developer" {
		t.Errorf("title = %q, want %q", p.Title, "Senior Developer")
	}
	if p.Company != "Acme" {
		t.Errorf("company = %q, want %q", p.Company, "Acme")
	}
	if p.Text != "<p>Senior Developer role at Acme.</p>" {
		t.Errorf("text = %q", p.Text)
	}
	if p.TextState != listing.TextFull {
		t.Errorf("text state = %q, want full", p.TextState)
	}
	if want := []string{"Engineering", "Backend"}; len(p.Tags) != len(want) {
		t.Errorf("tags = %v, want %v", p.Tags, want)
	}
	wantDate := time.Date(2024, 3, 11, 10, 0, 0, 0, time.UTC)
	if !p.Published.Equal(wantDate) {
		t.Errorf("published = %v, want %v", p.Published, wantDate)
	}
	if p.PublishedBasis != "pubDate" {
		t.Errorf("basis = %q, want pubDate", p.PublishedBasis)
	}
	if want := "http://feeds.acme.example.com/post/1"; p.URL != want {
		t.Errorf("URL = %q, want %q (relative link not resolved)", p.URL, want)
	}

	p2 := postings[1]
	if p2.Company != "Example Co" {
		t.Errorf("item2 company = %q, want %q", p2.Company, "Example Co")
	}
	if p2.Title != "Job at Example Co" {
		t.Errorf("item2 title = %q", p2.Title)
	}
}

func TestFeed_Atom_maps_all_fields(t *testing.T) {
	body := mustReadFile(t, "testdata/atom.xml")
	feedURL := "http://jobs.example.com/atom"
	m := fetchtest.NewMap()
	m.Set(feedURL, fetchtest.MapEntry{
		Response: fetch.Response{URL: feedURL, Status: 200, Body: body},
	})
	cfg := source.SiteConfig{URL: feedURL}
	postings, err := source.For(mustParseURL(t, feedURL)).List(context.Background(), m, cfg)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(postings) != 1 {
		t.Fatalf("got %d postings, want 1", len(postings))
	}
	p := postings[0]
	if p.Title != "DevOps Engineer" {
		t.Errorf("title = %q", p.Title)
	}
	if want := "http://jobs.example.com/devops-engineer"; p.URL != want {
		t.Errorf("URL = %q, want %q", p.URL, want)
	}
	if p.Text != "<p>DevOps role at Example Corp.</p>" {
		t.Errorf("text = %q", p.Text)
	}
	wantDate := time.Date(2024, 3, 5, 12, 0, 0, 0, time.UTC)
	if !p.Published.Equal(wantDate) {
		t.Errorf("published = %v, want %v (should use published, not updated)", p.Published, wantDate)
	}
	notUpdated := time.Date(2024, 3, 10, 12, 0, 0, 0, time.UTC)
	if p.Published.Equal(notUpdated) {
		t.Errorf("published uses updated date %v; want first published date", notUpdated)
	}
	if p.PublishedBasis != "published" {
		t.Errorf("basis = %q, want published", p.PublishedBasis)
	}
	if want := []string{"Infrastructure", "Cloud"}; len(p.Tags) != len(want) || p.Tags[0] != want[0] {
		t.Errorf("tags = %v, want %v", p.Tags, want)
	}
}

func TestFeed_title_split(t *testing.T) {
	for _, tc := range []struct {
		name    string
		xml     string
		title   string
		company string
	}{
		{
			name:    "RSS Role at Company split at last at",
			xml:     `<rss version="2.0"><channel><item><title>Engineer at Acme</title><link>http://x.example.com/1</link></item></channel></rss>`,
			title:   "Engineer",
			company: "Acme",
		},
		{
			name:    "RSS multiple at uses last",
			xml:     `<rss version="2.0"><channel><item><title>Senior at Acme at Work</title><link>http://x.example.com/2</link></item></channel></rss>`,
			title:   "Senior at Acme",
			company: "Work",
		},
		{
			name:    "RSS no at left whole",
			xml:     `<rss version="2.0"><channel><item><title>Senior Engineer</title><link>http://x.example.com/3</link></item></channel></rss>`,
			title:   "Senior Engineer",
			company: "",
		},
		{
			name:    "Atom Role at Company split",
			xml:     `<feed xmlns="http://www.w3.org/2005/Atom"><entry><title>Designer at Studio</title><link href="http://x.example.com/4"/><published>2024-01-01T00:00:00Z</published></entry></feed>`,
			title:   "Designer",
			company: "Studio",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr := "http://x.example.com/feed"
			m := fetchtest.NewMap()
			m.Set(addr, fetchtest.MapEntry{Response: fetch.Response{URL: addr, Status: 200, ContentType: "application/rss+xml", Body: []byte(tc.xml)}})
			ps, err := source.For(mustParseURL(t, addr)).List(context.Background(), m, source.SiteConfig{URL: addr})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(ps) != 1 {
				t.Fatalf("got %d postings, want 1", len(ps))
			}
			if ps[0].Title != tc.title {
				t.Errorf("title = %q, want %q", ps[0].Title, tc.title)
			}
			if ps[0].Company != tc.company {
				t.Errorf("company = %q, want %q", ps[0].Company, tc.company)
			}
		})
	}
}

func TestGreenhouse_maps_all_fields(t *testing.T) {
	body := mustReadFile(t, "testdata/greenhouse.json")
	apiURL := "https://boards-api.greenhouse.io/v1/boards/brightway/jobs?content=true"
	m := fetchtest.NewMap()
	m.Set(apiURL, fetchtest.MapEntry{
		Response: fetch.Response{URL: apiURL, Status: 200, Body: body},
	})
	cfg := source.SiteConfig{Name: "Brightway Software", URL: "https://boards.greenhouse.io/brightway"}
	postings, err := source.For(mustParseURL(t, cfg.URL)).List(context.Background(), m, cfg)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(postings) != 2 {
		t.Fatalf("got %d postings, want 2", len(postings))
	}

	p := postings[0]
	if p.URL != "https://boards.greenhouse.io/brightway/jobs/1001" {
		t.Errorf("URL = %q", p.URL)
	}
	if p.ApplyURL != "https://boards.greenhouse.io/brightway/jobs/1001" {
		t.Errorf("ApplyURL = %q", p.ApplyURL)
	}
	if p.Title != "Backend Engineer" {
		t.Errorf("title = %q", p.Title)
	}
	if p.Company != "Brightway Corp" {
		t.Errorf("company = %q, want Brightway Corp (from company_name)", p.Company)
	}
	if p.Location != "Remote" {
		t.Errorf("location = %q", p.Location)
	}
	if len(p.Tags) != 1 || p.Tags[0] != "Engineering" {
		t.Errorf("tags = %v, want [Engineering]", p.Tags)
	}
	wantDate := time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)
	if !p.Published.Equal(wantDate) {
		t.Errorf("published = %v, want %v", p.Published, wantDate)
	}
	notUpdated := time.Date(2024, 3, 15, 12, 0, 0, 0, time.UTC)
	if p.Published.Equal(notUpdated) {
		t.Errorf("published uses updated_at %v; must use first_published only", notUpdated)
	}
	if p.PublishedBasis != "first_published" {
		t.Errorf("basis = %q, want first_published", p.PublishedBasis)
	}
	if p.Text == "" {
		t.Errorf("text is empty")
	}
	if p.TextState != listing.TextFull {
		t.Errorf("text state = %q, want full", p.TextState)
	}

	if p.CompanyGuessed {
		t.Error("CompanyGuessed should be false when company_name is set")
	}

	p2 := postings[1]
	if p2.Company != "Brightway Software" {
		t.Errorf("item2 company = %q, want Brightway Software (board name fallback)", p2.Company)
	}
	if len(p2.Tags) != 2 {
		t.Errorf("item2 tags = %v, want 2 departments", p2.Tags)
	}
	if !p2.CompanyGuessed {
		t.Error("CompanyGuessed should be true when company_name is absent")
	}
}

func TestLever_maps_all_fields(t *testing.T) {
	body := mustReadFile(t, "testdata/lever.json")
	apiURL := "https://api.lever.co/v0/postings/techstartup?mode=json"
	m := fetchtest.NewMap()
	m.Set(apiURL, fetchtest.MapEntry{
		Response: fetch.Response{URL: apiURL, Status: 200, Body: body},
	})
	cfg := source.SiteConfig{Name: "TechStartup", URL: "https://jobs.lever.co/techstartup"}
	postings, err := source.For(mustParseURL(t, cfg.URL)).List(context.Background(), m, cfg)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(postings) != 1 {
		t.Fatalf("got %d postings, want 1", len(postings))
	}
	p := postings[0]
	if p.URL != "https://jobs.lever.co/techstartup/abc-def-123" {
		t.Errorf("URL = %q", p.URL)
	}
	if p.ApplyURL != p.URL {
		t.Errorf("ApplyURL = %q, want same as URL (hostedUrl)", p.ApplyURL)
	}
	if p.Title != "Full Stack Developer" {
		t.Errorf("title = %q", p.Title)
	}
	if p.Company != "TechStartup" {
		t.Errorf("company = %q, want TechStartup (source name)", p.Company)
	}
	if p.Location != "San Francisco" {
		t.Errorf("location = %q", p.Location)
	}
	if len(p.Tags) < 2 {
		t.Errorf("tags = %v, want at least team and department", p.Tags)
	}
	if p.Fields["employment_type"] != "Full-time" {
		t.Errorf("employment_type = %q", p.Fields["employment_type"])
	}
	if p.Fields["workplace_type"] != "hybrid" {
		t.Errorf("workplace_type = %q", p.Fields["workplace_type"])
	}
	if p.Fields["salary"] == "" {
		t.Errorf("salary field empty")
	}
	wantDate := time.UnixMilli(1709280000000).UTC()
	if !p.Published.Equal(wantDate) {
		t.Errorf("published = %v, want %v", p.Published, wantDate)
	}
	if p.PublishedBasis != "createdAt" {
		t.Errorf("basis = %q, want createdAt", p.PublishedBasis)
	}
	if p.Text == "" {
		t.Errorf("text is empty")
	}
	for _, want := range []string{"<p>We are building great things.</p>", "<h3>Requirements</h3>", "<ul>", "<p>Apply via our website.</p>"} {
		if !strings.Contains(p.Text, want) {
			t.Errorf("text missing %q", want)
		}
	}
	if p.CompanyGuessed {
		t.Error("CompanyGuessed should be false when source name is set")
	}
}

func TestLever_company_falls_back_to_site_slug(t *testing.T) {
	apiURL := "https://api.lever.co/v0/postings/techstartup?mode=json"
	m := fetchtest.NewMap()
	m.Set(apiURL, fetchtest.MapEntry{
		Response: fetch.Response{URL: apiURL, Status: 200, Body: mustReadFile(t, "testdata/lever.json")},
	})
	cfg := source.SiteConfig{Name: "", URL: "https://jobs.lever.co/techstartup"}
	postings, err := source.For(mustParseURL(t, cfg.URL)).List(context.Background(), m, cfg)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(postings) == 0 {
		t.Fatal("no postings")
	}
	if postings[0].Company != "techstartup" {
		t.Errorf("company = %q, want techstartup (site slug)", postings[0].Company)
	}
	if !postings[0].CompanyGuessed {
		t.Error("CompanyGuessed should be true without source name")
	}
}

func TestAshby_maps_all_fields(t *testing.T) {
	body := mustReadFile(t, "testdata/ashby.json")
	apiURL := "https://api.ashbyhq.com/posting-api/job-board/nexgen?includeCompensation=true"
	m := fetchtest.NewMap()
	m.Set(apiURL, fetchtest.MapEntry{
		Response: fetch.Response{URL: apiURL, Status: 200, Body: body},
	})
	cfg := source.SiteConfig{Name: "NexGen", URL: "https://jobs.ashbyhq.com/nexgen"}
	postings, err := source.For(mustParseURL(t, cfg.URL)).List(context.Background(), m, cfg)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(postings) != 1 {
		t.Fatalf("got %d postings, want 1 (unlisted skipped)", len(postings))
	}
	p := postings[0]
	if p.URL != "https://jobs.ashbyhq.com/nexgen/xyz-123" {
		t.Errorf("URL = %q", p.URL)
	}
	if p.ApplyURL != "https://jobs.ashbyhq.com/nexgen/xyz-123/application" {
		t.Errorf("ApplyURL = %q", p.ApplyURL)
	}
	if p.Title != "Data Scientist" {
		t.Errorf("title = %q", p.Title)
	}
	if p.Company != "NexGen" {
		t.Errorf("company = %q, want NexGen (source name)", p.Company)
	}
	if !strings.Contains(p.Location, "Austin, TX") || !strings.Contains(p.Location, "Remote") {
		t.Errorf("location = %q, want Austin TX + Remote", p.Location)
	}
	if len(p.Tags) < 2 {
		t.Errorf("tags = %v, want department + team", p.Tags)
	}
	if p.Fields["employment_type"] != "FullTime" {
		t.Errorf("employment_type = %q", p.Fields["employment_type"])
	}
	if p.Fields["salary"] != "$140K–$180K" {
		t.Errorf("salary = %q", p.Fields["salary"])
	}
	wantDate := time.Date(2024, 3, 10, 10, 0, 0, 0, time.UTC)
	if !p.Published.Equal(wantDate) {
		t.Errorf("published = %v, want %v", p.Published, wantDate)
	}
	if p.PublishedBasis != "publishedAt" {
		t.Errorf("basis = %q, want publishedAt", p.PublishedBasis)
	}
	if p.Text != "<p>Join our AI research team.</p>" {
		t.Errorf("text = %q", p.Text)
	}
	if p.CompanyGuessed {
		t.Error("CompanyGuessed should be false when source name is set")
	}
}

func TestAshby_company_falls_back_to_board_name(t *testing.T) {
	apiURL := "https://api.ashbyhq.com/posting-api/job-board/nexgen?includeCompensation=true"
	m := fetchtest.NewMap()
	m.Set(apiURL, fetchtest.MapEntry{
		Response: fetch.Response{URL: apiURL, Status: 200, Body: mustReadFile(t, "testdata/ashby.json")},
	})
	cfg := source.SiteConfig{Name: "", URL: "https://jobs.ashbyhq.com/nexgen"}
	postings, err := source.For(mustParseURL(t, cfg.URL)).List(context.Background(), m, cfg)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(postings) == 0 {
		t.Fatal("no postings")
	}
	if postings[0].Company != "NexGen AI" {
		t.Errorf("company = %q, want NexGen AI (org name from API)", postings[0].Company)
	}
	if !postings[0].CompanyGuessed {
		t.Error("CompanyGuessed should be true without source name")
	}
}

func TestHN_list(t *testing.T) {
	searchURL := "https://hn.algolia.com/api/v1/search_by_date?tags=story,author_whoishiring&hitsPerPage=5"
	storyURL := "https://hn.algolia.com/api/v1/items/42"
	searchBody := `{"hits":[{"objectID":"99","title":"Ask HN: Who wants to be hired? (Jan 2026)"},{"objectID":"42","title":"Ask HN: Who is hiring? (Jan 2026)"}]}`
	storyBody := `{"id":42,"children":[
		{"id":101,"text":"Acme Corp | <a href=\"https:&#x2F;&#x2F;acme.example.com\">https:&#x2F;&#x2F;acme.example.com</a> | acme.example | Engineer &amp; Lead | Berlin<p>More text","created_at":"2026-01-01T00:00:00Z","children":[]},
		{"id":102,"text":"No pipes here","created_at":"2026-01-02T00:00:00Z","children":[]},
		{"id":103,"text":"","created_at":"2026-01-03T00:00:00Z","children":[]},
		{"id":104,"text":"Zeta Inc | Remote | Contract","created_at":"2026-01-04T00:00:00Z","children":[]}
	]}`
	m := fetchtest.NewMap()
	m.Set(searchURL, fetchtest.MapEntry{Response: fetch.Response{URL: searchURL, Status: 200, Body: []byte(searchBody)}})
	m.Set(storyURL, fetchtest.MapEntry{Response: fetch.Response{URL: storyURL, Status: 200, Body: []byte(storyBody)}})
	cfg := source.SiteConfig{URL: "https://news.ycombinator.com/"}
	ps, err := source.For(mustParseURL(t, cfg.URL)).List(context.Background(), m, cfg)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ps) != 1 {
		t.Fatalf("got %d postings, want 1 (no-pipe, deleted and no-title skipped)", len(ps))
	}
	if ps[0].Company != "Acme Corp" {
		t.Errorf("company = %q", ps[0].Company)
	}
	if ps[0].Title != "Engineer & Lead" {
		t.Errorf("title = %q", ps[0].Title)
	}
	if ps[0].Location != "Berlin" {
		t.Errorf("location = %q, want Berlin", ps[0].Location)
	}
	if reqs := m.Requests(); len(reqs) != 2 {
		t.Errorf("requests = %v, want exactly 2", reqs)
	}
	if !strings.Contains(ps[0].URL, "101") {
		t.Errorf("URL = %q, want comment id 101", ps[0].URL)
	}
}

func TestDetail_company_recovery(t *testing.T) {
	cases := []struct {
		name, body, title, wantCo string
	}{
		{
			"json-ld no url matched by title",
			`<html><head><script type="application/ld+json">[{"@type":"JobPosting","title":"Rust Engineer","hiringOrganization":{"name":"Chain Labs"}},{"@type":"JobPosting","title":"Community Manager","hiringOrganization":{"name":"Other"}}]</script></head><body><main><p>b</p></main></body></html>`,
			"Rust Engineer", "Chain Labs",
		},
		{
			"page title fallback when no json-ld item matches",
			`<html><head><title>Rust Engineer at Chain Labs | site</title><script type="application/ld+json">[{"@type":"JobPosting","title":"Community Manager","hiringOrganization":{"name":"Chain Labs"}}]</script></head><body><p>b</p></body></html>`,
			"Rust Engineer", "Chain Labs",
		},
		{
			"ambiguous json-ld suppresses page title fallback",
			`<html><head><title>Rust Engineer at Chain Labs | site</title><script type="application/ld+json">[{"@type":"JobPosting","title":"Rust Engineer","hiringOrganization":{"name":"Chain A"}},{"@type":"JobPosting","title":"Rust Engineer","hiringOrganization":{"name":"Chain B"}}]</script></head></html>`,
			"Rust Engineer", "",
		},
		{
			"page title fills company when no json-ld present",
			`<html><head><title>Smart Contract Dev at DeFi Labs</title></head><body><main><p>desc</p></main></body></html>`,
			"Smart Contract Dev", "DeFi Labs",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const pageURL = "https://jobs.example.com/vacancy/12345"
			m := fetchtest.NewMap()
			m.Set(pageURL, fetchtest.MapEntry{Response: fetch.Response{URL: pageURL, Status: 200, Body: []byte(tc.body)}})
			p := listing.Posting{URL: pageURL, Title: tc.title}
			result, err := source.Detail(context.Background(), m, p, source.SiteConfig{URL: pageURL})
			if err != nil {
				t.Fatalf("Detail: %v", err)
			}
			if result.Company != tc.wantCo {
				t.Errorf("company = %q, want %q", result.Company, tc.wantCo)
			}
		})
	}
}

func mustParseURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", s, err)
	}
	return u
}

func mustReadFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", name, err)
	}
	return b
}
