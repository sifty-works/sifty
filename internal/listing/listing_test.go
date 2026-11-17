package listing_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/sifty-works/sifty/internal/listing"
)

// --- Canonical ---

func TestCanonical(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"lowercases scheme and host", "HTTPS://Example.COM/jobs/42", "https://example.com/jobs/42", false},
		{"drops www", "https://www.example.com/jobs", "https://example.com/jobs", false},
		{"drops fragment", "https://example.com/jobs/1#apply", "https://example.com/jobs/1", false},
		{"drops utm params", "https://example.com/jobs?utm_source=linkedin&utm_campaign=foo", "https://example.com/jobs", false},
		{"drops ref source gh_src", "https://example.com/jobs/1?ref=board&source=web&gh_src=abc&keep=1", "https://example.com/jobs/1?keep=1", false},
		{"sorts remaining query", "https://example.com/jobs?z=last&a=first", "https://example.com/jobs?a=first&z=last", false},
		{"drops trailing slash", "https://example.com/jobs/", "https://example.com/jobs", false},
		{"empty path becomes slash", "https://example.com", "https://example.com/", false},
		{"rejects non-HTTP", "ftp://example.com/file", "", true},
		{"case-insensitive param dropping", "https://example.com/jobs?UTM_SOURCE=x&REF=y&id=42", "https://example.com/jobs?id=42", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := listing.Canonical(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Errorf("Canonical(%q): expected error", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("Canonical(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("Canonical(%q): got %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// --- Identity ---

func TestIdentity(t *testing.T) {
	for _, tc := range []struct{ company, title, want string }{
		{"Acme Corp", "Software Engineer", "acme corp|software engineer"},
		{"Widgets", "Node.js Developer", "widgets|nodejs developer"},
		{"Widgets", "nodejs Developer", "widgets|nodejs developer"},
		{"Widgets", "Web3.js Lead", "widgets|web3js lead"},
		{"Widgets", "Layer 2 Engineer", "widgets|l2 engineer"},
		{"Widgets", "Chief Technology Officer", "widgets|cto"},
		{"Widgets", "Technical Lead", "widgets|tech lead"},
		{"Widgets", "Artificial Intelligence Researcher", "widgets|ai researcher"},
		{"Acme, Inc.", "Sr. Engineer (Remote)", "acme inc|sr engineer remote"},
		{"  Acme  ", "  Engineer  ", "acme|engineer"},
	} {
		got := listing.Identity(tc.company, tc.title)
		if got != tc.want {
			t.Errorf("Identity(%q, %q): got %q, want %q", tc.company, tc.title, got, tc.want)
		}
	}
}

// --- ConcreteATS ---

func TestConcreteATS(t *testing.T) {
	for _, tc := range []struct {
		raw    string
		want   string
		wantOK bool
	}{
		// Greenhouse job URLs matched and /apply suffix stripped.
		{"https://boards.greenhouse.io/acme/jobs/12345", "https://boards.greenhouse.io/acme/jobs/12345", true},
		{"https://job-boards.greenhouse.io/acme/jobs/12345", "https://job-boards.greenhouse.io/acme/jobs/12345", true},
		{"https://boards.greenhouse.io/acme/jobs/12345/apply", "https://boards.greenhouse.io/acme/jobs/12345", true},
		// Lever.
		{"https://jobs.lever.co/acme/550e8400-e29b-41d4-a716-446655440000", "https://jobs.lever.co/acme/550e8400-e29b-41d4-a716-446655440000", true},
		{"https://jobs.lever.co/acme/550e8400-e29b-41d4-a716-446655440000/apply", "https://jobs.lever.co/acme/550e8400-e29b-41d4-a716-446655440000", true},
		// Ashby — /apply and /application both stripped.
		{"https://jobs.ashbyhq.com/acme/550e8400-e29b-41d4-a716-446655440000", "https://jobs.ashbyhq.com/acme/550e8400-e29b-41d4-a716-446655440000", true},
		{"https://jobs.ashbyhq.com/acme/550e8400-e29b-41d4-a716-446655440000/apply", "https://jobs.ashbyhq.com/acme/550e8400-e29b-41d4-a716-446655440000", true},
		{"https://jobs.ashbyhq.com/acme/550e8400-e29b-41d4-a716-446655440000/application", "https://jobs.ashbyhq.com/acme/550e8400-e29b-41d4-a716-446655440000", true},
		// Workable.
		{"https://apply.workable.com/acme/j/ABC123", "https://apply.workable.com/acme/j/ABC123", true},
		{"https://apply.workable.com/acme/j/ABC123/apply", "https://apply.workable.com/acme/j/ABC123", true},
		// Board roots are rejected.
		{"https://boards.greenhouse.io/acme", "", false},
		{"https://job-boards.greenhouse.io/acme", "", false},
		{"https://jobs.lever.co/acme", "", false},
		{"https://jobs.ashbyhq.com/acme", "", false},
		{"https://apply.workable.com/acme", "", false},
		{"https://apply.workable.com/acme/j", "", false},
		// Other hosts are rejected.
		{"https://example.com/jobs/42", "", false},
		{"https://linkedin.com/jobs/view/12345", "", false},
	} {
		got, ok := listing.ConcreteATS(tc.raw)
		if ok != tc.wantOK {
			t.Errorf("ConcreteATS(%q) ok=%v, want %v", tc.raw, ok, tc.wantOK)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("ConcreteATS(%q): got %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// --- LegacyKey ---

func hexDigest(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// TestLegacyKey checks that LegacyKey produces SHA-256("url:"+legacyCanonical)
// for inputs that exercise the four differences from Canonical: preserved path
// percent-encoding, empty query values written as k=, only one trailing slash
// removed, and host case kept.
func TestLegacyKey(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string // hexDigest("url:" + legacyCanonical(raw))
	}{
		{
			"percent-encoded path preserved",
			"https://example.com/jobs/software%20engineer",
			hexDigest("url:https://example.com/jobs/software%20engineer"),
		},
		{
			"empty query value written as k=",
			"https://example.com/jobs?track=&id=42",
			hexDigest("url:https://example.com/jobs?id=42&track="),
		},
		{
			"double trailing slash: only one removed",
			"https://example.com/jobs//",
			hexDigest("url:https://example.com/jobs/"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := listing.LegacyKey(tc.raw)
			if got != tc.want {
				t.Errorf("LegacyKey(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
