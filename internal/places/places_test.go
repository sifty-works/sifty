package places_test

import (
	"slices"
	"testing"

	"github.com/sifty-works/sifty-server/internal/places"
)

func TestResolve_worldwide_phrases(t *testing.T) {
	for _, label := range []string{"Anywhere in the World", "Worldwide", "anywhere in the world"} {
		t.Run(label, func(t *testing.T) {
			p := places.Resolve(label)
			if !p.Worldwide {
				t.Errorf("Resolve(%q).Worldwide = false, want true", label)
			}
		})
	}
}

func TestResolve_usa_only(t *testing.T) {
	p := places.Resolve("USA Only")
	if p.Worldwide {
		t.Error("USA Only should not be worldwide")
	}
	if !slices.Contains(p.Countries, "US") {
		t.Errorf("countries = %v, want US", p.Countries)
	}
}

func TestResolve_europe_only(t *testing.T) {
	p := places.Resolve("Europe Only")
	if p.Worldwide {
		t.Error("Europe Only should not be worldwide")
	}
	if !slices.Contains(p.Countries, "DE") {
		t.Errorf("countries = %v, want DE (Europe member)", p.Countries)
	}
}

func TestResolve_asia_only(t *testing.T) {
	p := places.Resolve("Asia Only")
	if p.Worldwide {
		t.Error("Asia Only should not be worldwide")
	}
	if !slices.Contains(p.Countries, "JP") {
		t.Errorf("countries = %v, want JP (Asia member)", p.Countries)
	}
}

func TestResolve_oceania_only(t *testing.T) {
	p := places.Resolve("Oceania Only")
	if p.Worldwide {
		t.Error("Oceania Only should not be worldwide")
	}
	if !slices.Contains(p.Countries, "AU") {
		t.Errorf("countries = %v, want AU (Oceania member)", p.Countries)
	}
}

func TestResolve_latam_and_apac(t *testing.T) {
	for _, tc := range []struct {
		label   string
		country string
	}{
		{"LATAM", "BR"},
		{"APAC", "JP"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			p := places.Resolve(tc.label)
			if !slices.Contains(p.Countries, tc.country) {
				t.Errorf("Resolve(%q).Countries = %v, want %q", tc.label, p.Countries, tc.country)
			}
		})
	}
}
