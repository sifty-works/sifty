package inbox

import (
	"context"
	"fmt"
	"time"
)

// Row is one vacancy row in the API response.
type Row struct {
	ID        int64  `json:"id"`
	Company   string `json:"company"`
	Title     string `json:"title"`
	Location  string `json:"location"`
	Published string `json:"published"`
}

// list returns the rows, total count, and empty text for the given query.
// now provides the current time (and its Location is used for date formatting).
func list(ctx context.Context, r *repo, now func() time.Time, q string) (rows []Row, total int, err error) {
	total, err = r.count(ctx)
	if err != nil {
		return nil, 0, err
	}

	var raw []repoRow
	if q == "" {
		raw, err = r.all(ctx)
		if err != nil {
			return nil, 0, err
		}
	} else {
		raw, err = r.searchMatches(ctx, q)
		if err != nil {
			return nil, 0, err
		}
	}

	t := now()
	rows = make([]Row, len(raw))
	for i, rr := range raw {
		company := rr.company
		if company == "" {
			company = "Unknown"
		}
		rows[i] = Row{
			ID:        rr.id,
			Company:   company,
			Title:     rr.title,
			Location:  locationText(rr.location),
			Published: publicationLine(rr.publishedAt, rr.publishedBasis, t),
		}
	}
	return rows, total, nil
}

// locationText returns the location string or "Location unstated".
func locationText(loc string) string {
	if loc == "" {
		return "Location unstated"
	}
	return loc
}

// publicationLine formats the publication line for a vacancy.
// The timezone of now is used for date formatting.
func publicationLine(publishedAt, publishedBasis string, now time.Time) string {
	if publishedAt == "" {
		return "Publication date not reported"
	}
	t, err := time.Parse(time.RFC3339, publishedAt)
	if err != nil {
		return "Publication date not reported"
	}
	loc := now.Location()
	dateStr := t.In(loc).Format("2 Jan 2006")
	if publishedBasis != "" {
		return "Published " + dateStr + " · " + ageText(t, now)
	}
	return "Source date " + dateStr + " (unverified)"
}

// ageText returns a human-readable age string for the age of published relative to now.
func ageText(published, now time.Time) string {
	loc := now.Location()
	py, pm, pd := published.In(loc).Date()
	ny, nm, nd := now.In(loc).Date()
	pubDate := time.Date(py, pm, pd, 0, 0, 0, 0, loc)
	nowDate := time.Date(ny, nm, nd, 0, 0, 0, 0, loc)
	days := int(nowDate.Sub(pubDate).Hours() / 24)
	if days < 0 {
		days = 0
	}
	switch {
	case days == 0:
		return "today"
	case days == 1:
		return "1 day"
	case days < 14:
		return fmt.Sprintf("%d days", days)
	case days < 63:
		return fmt.Sprintf("%d weeks", days/7)
	default:
		months := days / 30
		if months < 1 {
			months = 1
		}
		return fmt.Sprintf("%d months", months)
	}
}

// emptyText returns the empty-state text for the response.
func emptyText(rowCount int, q string) string {
	if rowCount > 0 {
		return ""
	}
	if q != "" {
		return "No matching roles. Adjust your filters."
	}
	return "Inbox zero. Collect more jobs in Settings → Sources."
}
