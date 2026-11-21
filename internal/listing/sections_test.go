package listing_test

import (
	"strings"
	"testing"

	"github.com/sifty-works/sifty-server/internal/listing"
)

func TestReadOffer_CurlyApostropheAndColonInOfferHeading(t *testing.T) {
	body := "<p>What’s in it for you:</p><ul><li>Gym membership</li></ul>"
	offer := listing.ReadOffer(listing.Normalize(body))
	if !offer.Found {
		t.Fatal("offer section not found after normalising paragraph heading with curly apostrophe and colon")
	}
	if len(offer.Items) == 0 {
		t.Error("offer section has no items")
	}
}

func TestNormalize_OfferHeadingParagraphPromotedAndFoundByReadOffer(t *testing.T) {
	body := `<p>Base salary</p><p>120,000–140,000 USD</p>`
	out := listing.Normalize(body)
	if !strings.Contains(out, "<h3>Base salary</h3>") {
		t.Fatalf("Base salary not promoted to heading in: %s", out)
	}
	if !listing.ReadOffer(out).Found {
		t.Error("ReadOffer did not find the offer section")
	}
}

func TestReadOffer_TwoOfferHeadingsInRowBothCollected(t *testing.T) {
	body := `<h3>What we offer</h3><ul><li>Free lunch Fridays</li></ul>` +
		`<p>Why join us</p><ul><li>Amazing culture</li></ul>`
	offer := listing.ReadOffer(listing.Normalize(body))
	if !offer.Found {
		t.Fatal("offer section not found")
	}
	for _, want := range []string{"Free lunch Fridays", "Amazing culture"} {
		found := false
		for _, item := range offer.Items {
			if strings.Contains(item, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("item %q missing from offer items: %v", want, offer.Items)
		}
	}
}
