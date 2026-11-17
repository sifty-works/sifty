package listing_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/sifty-works/sifty/internal/listing"
)

// --- Canonical ---

func TestCanonical_LowercasesSchemeAndHost(t *testing.T) {
	got, err := listing.Canonical("HTTPS://Example.COM/jobs/42")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://example.com/jobs/42"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCanonical_DropsWWW(t *testing.T) {
	got, err := listing.Canonical("https://www.example.com/jobs")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://example.com/jobs"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCanonical_DropsFragment(t *testing.T) {
	got, err := listing.Canonical("https://example.com/jobs/1#apply")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://example.com/jobs/1"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCanonical_DropsUTMParams(t *testing.T) {
	got, err := listing.Canonical("https://example.com/jobs?utm_source=linkedin&utm_campaign=foo")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://example.com/jobs"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCanonical_DropsRefSourceGhSrc(t *testing.T) {
	got, err := listing.Canonical("https://example.com/jobs/1?ref=board&source=web&gh_src=abc&keep=1")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://example.com/jobs/1?keep=1"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCanonical_SortsRemainingQuery(t *testing.T) {
	got, err := listing.Canonical("https://example.com/jobs?z=last&a=first")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://example.com/jobs?a=first&z=last"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCanonical_DropsTrailingSlash(t *testing.T) {
	got, err := listing.Canonical("https://example.com/jobs/")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://example.com/jobs"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCanonical_EmptyPathBecomesSlash(t *testing.T) {
	got, err := listing.Canonical("https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://example.com/"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCanonical_RejectsNonHTTP(t *testing.T) {
	_, err := listing.Canonical("ftp://example.com/file")
	if err == nil {
		t.Error("expected error for ftp scheme")
	}
}

func TestCanonical_CaseInsensitiveParamDropping(t *testing.T) {
	got, err := listing.Canonical("https://example.com/jobs?UTM_SOURCE=x&REF=y&id=42")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://example.com/jobs?id=42"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- Identity ---

func TestIdentity_Basic(t *testing.T) {
	got := listing.Identity("Acme Corp", "Software Engineer")
	want := "acme corp|software engineer"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestIdentity_NormalisesNodejs(t *testing.T) {
	got := listing.Identity("Widgets", "Node.js Developer")
	want := "widgets|nodejs developer"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestIdentity_NormalisesNodejsAlreadyCanonical(t *testing.T) {
	got := listing.Identity("Widgets", "nodejs Developer")
	want := "widgets|nodejs developer"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestIdentity_NormalisesWeb3js(t *testing.T) {
	got := listing.Identity("Widgets", "Web3.js Lead")
	want := "widgets|web3js lead"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestIdentity_NormalisesLayerTwo(t *testing.T) {
	got := listing.Identity("Widgets", "Layer 2 Engineer")
	want := "widgets|l2 engineer"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestIdentity_NormalisesCTO(t *testing.T) {
	got := listing.Identity("Widgets", "Chief Technology Officer")
	want := "widgets|cto"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestIdentity_NormalisesTechnicalLead(t *testing.T) {
	got := listing.Identity("Widgets", "Technical Lead")
	want := "widgets|tech lead"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestIdentity_NormalisesAI(t *testing.T) {
	got := listing.Identity("Widgets", "Artificial Intelligence Researcher")
	want := "widgets|ai researcher"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestIdentity_CollapsesPunctuation(t *testing.T) {
	got := listing.Identity("Acme, Inc.", "Sr. Engineer (Remote)")
	want := "acme inc|sr engineer remote"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestIdentity_Trimmed(t *testing.T) {
	got := listing.Identity("  Acme  ", "  Engineer  ")
	want := "acme|engineer"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- ConcreteATS ---

func TestConcreteATS_GreenhouseBoards(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{
			"https://boards.greenhouse.io/acme/jobs/12345",
			"https://boards.greenhouse.io/acme/jobs/12345",
		},
		{
			"https://job-boards.greenhouse.io/acme/jobs/12345",
			"https://job-boards.greenhouse.io/acme/jobs/12345",
		},
		{
			"https://boards.greenhouse.io/acme/jobs/12345/apply",
			"https://boards.greenhouse.io/acme/jobs/12345",
		},
	}
	for _, c := range cases {
		got, ok := listing.ConcreteATS(c.raw)
		if !ok {
			t.Errorf("ConcreteATS(%q): expected match", c.raw)
			continue
		}
		if got != c.want {
			t.Errorf("ConcreteATS(%q): got %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestConcreteATS_Lever(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{
			"https://jobs.lever.co/acme/550e8400-e29b-41d4-a716-446655440000",
			"https://jobs.lever.co/acme/550e8400-e29b-41d4-a716-446655440000",
		},
		{
			"https://jobs.lever.co/acme/550e8400-e29b-41d4-a716-446655440000/apply",
			"https://jobs.lever.co/acme/550e8400-e29b-41d4-a716-446655440000",
		},
	}
	for _, c := range cases {
		got, ok := listing.ConcreteATS(c.raw)
		if !ok {
			t.Errorf("ConcreteATS(%q): expected match", c.raw)
			continue
		}
		if got != c.want {
			t.Errorf("ConcreteATS(%q): got %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestConcreteATS_Ashby(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{
			"https://jobs.ashbyhq.com/acme/550e8400-e29b-41d4-a716-446655440000",
			"https://jobs.ashbyhq.com/acme/550e8400-e29b-41d4-a716-446655440000",
		},
		{
			"https://jobs.ashbyhq.com/acme/550e8400-e29b-41d4-a716-446655440000/apply",
			"https://jobs.ashbyhq.com/acme/550e8400-e29b-41d4-a716-446655440000",
		},
		{
			"https://jobs.ashbyhq.com/acme/550e8400-e29b-41d4-a716-446655440000/application",
			"https://jobs.ashbyhq.com/acme/550e8400-e29b-41d4-a716-446655440000",
		},
	}
	for _, c := range cases {
		got, ok := listing.ConcreteATS(c.raw)
		if !ok {
			t.Errorf("ConcreteATS(%q): expected match", c.raw)
			continue
		}
		if got != c.want {
			t.Errorf("ConcreteATS(%q): got %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestConcreteATS_Workable(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{
			"https://apply.workable.com/acme/j/ABC123",
			"https://apply.workable.com/acme/j/ABC123",
		},
		{
			"https://apply.workable.com/acme/j/ABC123/apply",
			"https://apply.workable.com/acme/j/ABC123",
		},
	}
	for _, c := range cases {
		got, ok := listing.ConcreteATS(c.raw)
		if !ok {
			t.Errorf("ConcreteATS(%q): expected match", c.raw)
			continue
		}
		if got != c.want {
			t.Errorf("ConcreteATS(%q): got %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestConcreteATS_RejectsBoardRoots(t *testing.T) {
	roots := []string{
		"https://boards.greenhouse.io/acme",
		"https://job-boards.greenhouse.io/acme",
		"https://jobs.lever.co/acme",
		"https://jobs.ashbyhq.com/acme",
		"https://apply.workable.com/acme",
		"https://apply.workable.com/acme/j",
	}
	for _, r := range roots {
		_, ok := listing.ConcreteATS(r)
		if ok {
			t.Errorf("ConcreteATS(%q): should not match a board root", r)
		}
	}
}

func TestConcreteATS_RejectsOtherHosts(t *testing.T) {
	others := []string{
		"https://example.com/jobs/42",
		"https://linkedin.com/jobs/view/12345",
	}
	for _, u := range others {
		_, ok := listing.ConcreteATS(u)
		if ok {
			t.Errorf("ConcreteATS(%q): should not match", u)
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
