package listing

import (
	"strings"
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
//  3. Every run of non-alphanumeric characters becomes one space (via FTSNorm).
//  4. Trimmed.
//
// The result is "normalisedCompany|normalisedTitle".
func Identity(company, title string) string {
	return normaliseIdPart(company) + "|" + normaliseIdPart(title)
}

// normaliseIdPart applies the identity normalisation to one part (company or
// title). It uses FTSNorm so that the identity key's character collapsing is
// always consistent with the full-text index normalisation.
func normaliseIdPart(s string) string {
	s = strings.ToLower(s)
	s = applyPhrases(s)
	return strings.TrimSpace(FTSNorm(s))
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
