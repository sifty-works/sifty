package listing_test

import (
	"strings"
	"testing"

	"github.com/sifty-works/sifty-server/internal/listing"
)

func offerSummary(o listing.Offer) string {
	var parts []string
	if !o.Found {
		parts = append(parts, "no section")
	}
	for _, t := range o.Tiles {
		parts = append(parts, t.Key+"="+t.Value)
	}
	if len(o.Items) > 0 {
		parts = append(parts, "others: "+strings.Join(o.Items, " | "))
	}
	return strings.Join(parts, "; ")
}

func TestReadOffer_Sections(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"one tile per kind in the vocabulary's order",
			`<h3>Benefits</h3><ul><li>Flexible hours: a four-day week</li><li>Signing bonus of 5,000</li><li>Equity: 0.5%</li></ul>`,
			"equity=Equity: 0.5%; bonus=Signing bonus of 5,000; flexible=Flexible hours: a four-day week"},
		{"a benefit named without a value takes the not stated text",
			`<h2>Perks</h2><ul><li>Equity</li><li>401(k)</li></ul>`,
			"equity=Amount not stated; retirement=Plan not stated"},
		{"a second item of a taken kind goes to the other items",
			`<h2>Benefits</h2><ul><li>Private medical cover</li><li>Dental plan for your family</li></ul>`,
			"health=Private medical cover; others: Dental plan for your family"},
		{"items matching no kind keep their order",
			`<h3>What we offer</h3><ul><li>Free lunch on Fridays</li><li>A dog in every room</li></ul>`,
			"others: Free lunch on Fridays | A dog in every room"},
		{"a line break inside a paragraph starts another item",
			`<h3>Salary</h3><p>Yearly bonus<br>Free lunch</p>`,
			"bonus=Yearly bonus; others: Free lunch"},
		{"a heading that only contains an offer word opens a section",
			`<h2>Compensation and benefits</h2><ul><li>Stock options</li></ul>`,
			"equity=Amount not stated"},
		{"the section runs to the next heading",
			`<h3>Benefits</h3><ul><li>Equity</li></ul><h3>About us</h3><ul><li>Free snacks</li></ul>`,
			"equity=Amount not stated"},
		{"text without an offer heading opens no section",
			`<h3>About the role</h3><p>Equity</p>`, "no section"},
		{"text that was never fetched opens no section", ``, "no section"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := offerSummary(listing.ReadOffer(tc.body)); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
