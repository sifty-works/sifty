// Package reading holds what is known about a vacancy's conditions and reads
// them by code from the fields a source states, the location, the tags and the
// labelled lines of the text ("Salary: …", "Location: …").
package reading

import (
	_ "embed"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Vacancy is what code reads conditions from.
type Vacancy struct {
	Title    string
	Location string
	Tags     []string
	Fields   map[string]string // structured fields a source states: salary, employment_type, workplace_type
	Text     string            // plain text, one block per line
	Full     bool              // the full text is available
}

// Money is a stated cash range. A zero bound or an empty string is not stated.
type Money struct {
	Min, Max float64
	Currency string // ISO code
	Period   string // year, month, day, hour
	Tax      string // gross, net
}

// Hours is the stated working time; Min and Max are weekly hours, 0 when not stated.
type Hours struct {
	Mode     string // full_time, part_time
	Min, Max float64
}

// VacancyReading is what is known about a vacancy's conditions.
type VacancyReading struct {
	Title      string
	Full       bool
	Money      *Money
	WorkModes  []string // remote, hybrid, onsite
	Engagement string   // employment, contract
	Hours      *Hours
	Locations  []string // place labels as stated
	TimeZone   string   // a stated time zone as written
}

//go:embed labels.yaml
var wordsYAML []byte

var (
	labels, modes = loadWords()
	amountRe      = regexp.MustCompile(`(\d{1,3}(?:[,\s]\d{3})+|\d+(?:\.\d+)?)\s*([kK])?`)
	currencyRe    = regexp.MustCompile(`(?i)\b(USD|EUR|GBP|CAD|AUD|CHF)\b`)
	notCashRe     = regexp.MustCompile(`(?i)\b(bonus|equity|stock[- ]option|commission|estimate|survey|ote|allowance|relocation)\b`)
	weeklyRe      = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)(?:\s*(?:-|–|—|to)\s*(\d+(?:\.\d+)?))?\s*(?:h|hr|hrs|hours)\s*(?:/|per)?\s*week`)
)

// FromCode reads the conditions code can read: source fields first, then the
// location, the labelled lines of the text and the tags.
func FromCode(v Vacancy) VacancyReading {
	r := VacancyReading{Title: v.Title, Full: v.Full}
	if s := v.Fields["salary"]; s != "" {
		r.Money = money(s, s)
	}
	if t := strings.ReplaceAll(strings.ToLower(v.Fields["employment_type"]), "_", " "); t != "" {
		r.Engagement = engagement(t)
		r.setHours(t)
	}
	if m, ok := workMode(v.Fields["workplace_type"]); ok {
		r.addMode(m)
	}
	r.addPlaces(v.Location)
	for _, line := range strings.Split(v.Text, "\n") {
		i := strings.Index(line, ":")
		if i < 0 {
			continue
		}
		value := strings.TrimSpace(line[i+1:])
		switch labels[strings.ToLower(strings.Join(strings.Fields(line[:i]), " "))] {
		case "money":
			if r.Money == nil {
				r.Money = money(value, line)
			}
		case "location":
			r.addPlaces(value)
		case "mode":
			if m, ok := workMode(value); ok {
				r.addMode(m)
			}
		case "engagement":
			if r.Engagement == "" {
				r.Engagement = engagement(strings.ToLower(value))
			}
		case "hours":
			if r.Hours == nil {
				r.setHours(value)
			}
		case "zone":
			if r.TimeZone == "" {
				r.TimeZone = value
			}
		}
	}
	for _, t := range v.Tags {
		low := strings.ToLower(strings.TrimSpace(t))
		if m, ok := workMode(low); ok && len(r.WorkModes) == 0 {
			r.addMode(m)
		}
		if r.Hours == nil && (low == "full-time" || low == "part-time") {
			r.setHours(low)
		}
		if r.Engagement == "" && (low == "contract" || low == "freelance") {
			r.Engagement = "contract"
		}
	}
	return r
}

// addPlaces splits a location value; a part that is a work mode is a work mode.
func (r *VacancyReading) addPlaces(value string) {
	for _, part := range strings.FieldsFunc(value, func(c rune) bool { return c == '·' || c == ';' || c == '|' }) {
		part = strings.TrimSpace(part)
		if m, ok := workMode(part); ok {
			r.addMode(m)
		} else if part != "" && !hasFold(r.Locations, part) {
			r.Locations = append(r.Locations, part)
		}
	}
}

func (r *VacancyReading) addMode(m string) {
	if !hasFold(r.WorkModes, m) {
		r.WorkModes = append(r.WorkModes, m)
	}
}

// setHours reads full-time, part-time and weekly hours ("20–30 hours per week").
func (r *VacancyReading) setHours(value string) {
	low := strings.ToLower(value)
	h := Hours{}
	switch {
	case strings.Contains(low, "full-time") || strings.Contains(low, "full time"):
		h.Mode = "full_time"
	case strings.Contains(low, "part-time") || strings.Contains(low, "part time"):
		h.Mode = "part_time"
	}
	if m := weeklyRe.FindStringSubmatch(value); m != nil {
		h.Max, _ = strconv.ParseFloat(m[1], 64)
		if m[2] != "" {
			h.Min = h.Max
			h.Max, _ = strconv.ParseFloat(m[2], 64)
		}
	}
	if h != (Hours{}) {
		r.Hours = &h
	}
}

func workMode(value string) (string, bool) {
	m, ok := modes[strings.ToLower(strings.TrimSpace(strings.Trim(value, ".,:;")))]
	return m, ok
}

func loadWords() (map[string]string, map[string]string) {
	var v struct{ Labels, Modes map[string][]string }
	if err := yaml.Unmarshal(wordsYAML, &v); err != nil {
		panic(fmt.Sprintf("reading: load labels.yaml: %v", err))
	}
	flip := func(in map[string][]string) map[string]string {
		out := map[string]string{}
		for k, ws := range in {
			for _, w := range ws {
				out[w] = k
			}
		}
		return out
	}
	return flip(v.Labels), flip(v.Modes)
}

func engagement(low string) string {
	switch {
	case strings.Contains(low, "contract") || strings.Contains(low, "freelance"):
		return "contract"
	case strings.Contains(low, "employment") || strings.Contains(low, "permanent") ||
		strings.Contains(low, "full-time") || strings.Contains(low, "full time") || strings.Contains(low, "employee"):
		return "employment"
	}
	return ""
}

// money reads a cash statement such as "USD 120,000 – 150,000 per year gross".
// line is the whole line, which may name the currency, period and tax basis.
func money(value, line string) *Money {
	low := strings.ToLower(line)
	salary := strings.Contains(low, "salary") || strings.Contains(low, " base ") || strings.Contains(low, "wage")
	if !salary && notCashRe.MatchString(low) {
		return nil
	}
	m := &Money{Currency: currency(value), Period: period(low)}
	if m.Currency == "" {
		m.Currency = currency(line)
	}
	if strings.Contains(low, "gross") {
		m.Tax = "gross"
	} else if strings.Contains(low, "net") {
		m.Tax = "net"
	}
	var amounts []float64
	idx := amountRe.FindAllStringSubmatchIndex(value, -1)
	for _, x := range idx {
		n, err := strconv.ParseFloat(strings.NewReplacer(",", "", " ", "").Replace(value[x[2]:x[3]]), 64)
		if err != nil {
			return nil
		}
		if x[4] >= 0 {
			n *= 1000
		}
		amounts = append(amounts, n)
	}
	switch {
	case len(amounts) > 2:
		if len(amounts)%2 != 0 {
			return nil
		}
		m.Min, m.Max = amounts[0], amounts[0]
		for _, a := range amounts {
			m.Min, m.Max = min(m.Min, a), max(m.Max, a)
		}
		if m.Min < 1000 || m.Max > 10_000_000 {
			return nil
		}
	case len(amounts) == 2:
		between := strings.ToLower(strings.Trim(value[idx[0][1]:idx[1][0]], "$€£ \t,"))
		if between != "-" && between != "–" && between != "—" && between != "to" {
			return nil
		}
		m.Min, m.Max = min(amounts[0], amounts[1]), max(amounts[0], amounts[1])
	case len(amounts) == 1 && (hasAny(low, " from ", "starting at", "starting from", "minimum", "at least", ">=") || strings.HasPrefix(low, "from ")):
		m.Min = amounts[0] // a floor alone is not a top of the range
	case len(amounts) == 1:
		m.Max = amounts[0]
	default:
		return nil
	}
	return m
}

func currency(s string) string {
	if c := currencyRe.FindString(s); c != "" {
		return strings.ToUpper(c)
	}
	for _, sc := range [][2]string{{"$", "USD"}, {"€", "EUR"}, {"£", "GBP"}} {
		if strings.Contains(s, sc[0]) {
			return sc[1]
		}
	}
	return ""
}

func period(low string) string {
	switch {
	case hasAny(low, "per year", "/year", "annual", "p.a."):
		return "year"
	case hasAny(low, "per month", "/month", "monthly"):
		return "month"
	case hasAny(low, "per day", "/day", "daily"):
		return "day"
	case hasAny(low, "per hour", "/hour", "hourly"):
		return "hour"
	}
	return ""
}

func hasAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func hasFold(xs []string, x string) bool {
	for _, y := range xs {
		if strings.EqualFold(strings.Join(strings.Fields(y), " "), strings.Join(strings.Fields(x), " ")) {
			return true
		}
	}
	return false
}
