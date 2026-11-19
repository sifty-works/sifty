// Package listing defines what a source hands over and the one path
// from any input to stored HTML.
package listing

import "time"

// TextState describes the state of a vacancy's text.
type TextState string

const (
	TextFull       TextState = "full"
	TextSummary    TextState = "summary"
	TextBlocked    TextState = "blocked"
	TextNotFetched TextState = "not_fetched"
	TextRemoved    TextState = "removed"
)

// Posting is what a source hands over for a single vacancy.
type Posting struct {
	URL      string // original address
	ApplyURL string // apply address (optional)
	Title    string
	Company  string
	// CompanyGuessed is true when the source derived the company from the board's
	// address (e.g. Lever or Ashby without a configured source name, or Greenhouse
	// without a per-job company_name). A guessed company is used for a new vacancy
	// but never replaces a stored company that is already named.
	CompanyGuessed bool
	Location       string
	Tags           []string
	Fields         map[string]string // structured fields (salary, employment type…)

	Published      time.Time // only a stated publication date
	PublishedBasis string    // e.g. "datePosted", "pubDate"
	Expires        time.Time

	Text      string
	TextState TextState

	// Requested is set by the collector when source.Detail was called for this
	// posting. Upsert records the request time when it is true.
	Requested bool
}
