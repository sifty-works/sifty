// Package listing defines what a source hands over and the one path
// from any input to stored HTML.
package listing

import "time"

// Kind describes the format of the raw text in a Posting.
type Kind int

const (
	HTML        Kind = iota // HTML markup
	EscapedHTML             // HTML-escaped markup (e.g. &lt;p&gt;)
	Markdown                // Markdown
	Plain                   // plain text
)

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
	Location string
	Tags     []string
	Fields   map[string]string // structured fields (salary, employment type…)

	Published      time.Time // only a stated publication date
	PublishedBasis string    // e.g. "datePosted", "pubDate"
	Expires        time.Time

	Text      string
	TextKind  Kind
	TextState TextState
}
