package listing

import (
	"strings"
	"unicode"
)

// Identity returns a normalised "company|title" string used for
// deduplication of vacancies from different sources.
//
// Normalisation steps (applied to both parts before joining):
//  1. Lowercase.
//  2. Known phrase replacements (order matters — longest first to avoid partial matches):
//     "chief technology officer" → "cto"
//     "artificial intelligence"  → "ai"
//     "technical lead"            → "tech lead"
//     "node.js" / "nodejs"        → "nodejs"
//     "web3.js"                   → "web3js"
//     "layer 2"                   → "l2"
//  3. Every run of non-alphanumeric characters becomes one space.
//  4. Trimmed.
//
// The result is "normalisedCompany|normalisedTitle".
func Identity(company, title string) string {
	return normaliseIdPart(company) + "|" + normaliseIdPart(title)
}

func normaliseIdPart(s string) string {
	s = strings.ToLower(s)
	s = applyPhrases(s)
	s = collapseNonAlnum(s)
	s = strings.TrimSpace(s)
	return s
}

// phrase is an exact-string replacement applied after lowercasing.
type phrase struct{ from, to string }

// phrases are tried in order; longer phrases come first so they shadow
// sub-phrases.
var phrases = []phrase{
	{"chief technology officer", "cto"},
	{"artificial intelligence", "ai"},
	{"technical lead", "tech lead"},
	{"node.js", "nodejs"},
	{"web3.js", "web3js"},
	{"layer 2", "l2"},
}

func applyPhrases(s string) string {
	for _, p := range phrases {
		s = strings.ReplaceAll(s, p.from, p.to)
	}
	return s
}

// collapseNonAlnum replaces every run of non-alphanumeric runes with a
// single ASCII space.
func collapseNonAlnum(s string) string {
	var b strings.Builder
	inRun := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if inRun {
				b.WriteByte(' ')
				inRun = false
			}
			b.WriteRune(r)
		} else {
			inRun = true
		}
	}
	return b.String()
}
