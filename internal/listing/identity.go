package listing

import (
	"strings"
)

func Identity(company, title string) string {
	return normaliseIdPart(company) + "|" + normaliseIdPart(title)
}

func normaliseIdPart(s string) string {
	s = strings.ToLower(s)
	s = applyPhrases(s)
	return strings.TrimSpace(FTSNorm(s))
}

type phrase struct{ from, to string }

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
