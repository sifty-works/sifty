package listing

import (
	_ "embed"
	"strings"
	"unicode"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"gopkg.in/yaml.v3"
)

//go:embed offer.yaml
var offerYAML []byte

type OfferTile struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value string `json:"value"`
}

type Offer struct {
	Tiles []OfferTile
	Items []string
	Found bool
}

type offerKind struct {
	Key       string
	Label     string
	NotStated string `yaml:"notStated"`
	Keywords  []string
}

type offerVocabulary struct {
	Kinds []offerKind
}

var offerVocab = mustLoadOffer()

func mustLoadOffer() offerVocabulary {
	var v offerVocabulary
	if err := yaml.Unmarshal(offerYAML, &v); err != nil {
		panic("listing: load offer.yaml: " + err.Error())
	}
	return v
}

func ReadOffer(body string) Offer {
	nodes, err := xhtml.ParseFragment(strings.NewReader(body), &xhtml.Node{
		Type:     xhtml.ElementNode,
		Data:     "body",
		DataAtom: atom.Body,
	})
	if err != nil {
		return Offer{}
	}
	var items []string
	inSection, found := false, false
	for _, n := range nodes {
		if n.Type != xhtml.ElementNode {
			continue
		}
		switch n.DataAtom {
		case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
			inSection = offerHeading(strings.Join(blockLines(n), " "))
			found = found || inSection
		case atom.P, atom.Ul, atom.Ol:
			if inSection {
				items = append(items, blockLines(n)...)
			}
		}
	}
	return matchOffer(items, found)
}

func blockLines(n *xhtml.Node) []string {
	var b strings.Builder
	extractText(n, &b)
	var out []string
	for _, line := range strings.Split(b.String(), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func offerHeading(text string) bool {
	norm := normHeading(text)
	for _, e := range sections.Headings {
		if e.Kind == "offer" && norm == e.Text {
			return true
		}
	}
	for _, e := range sections.Contains {
		if e.Kind == "offer" && strings.Contains(norm, e.Text) {
			return true
		}
	}
	return false
}

func matchOffer(items []string, found bool) Offer {
	o := Offer{Found: found}
	values := make([]string, len(offerVocab.Kinds))
	for _, item := range items {
		i, keyword := matchKind(item)
		if i < 0 || values[i] != "" {
			o.Items = append(o.Items, item)
			continue
		}
		values[i] = offerValue(item, keyword, offerVocab.Kinds[i].NotStated)
	}
	for i, k := range offerVocab.Kinds {
		if values[i] != "" {
			o.Tiles = append(o.Tiles, OfferTile{Key: k.Key, Label: k.Label, Value: values[i]})
		}
	}
	return o
}

func matchKind(item string) (int, string) {
	low := strings.ToLower(item)
	for i, k := range offerVocab.Kinds {
		for _, keyword := range k.Keywords {
			if strings.Contains(low, strings.ToLower(keyword)) {
				return i, keyword
			}
		}
	}
	return -1, ""
}

func offerValue(item, keyword, notStated string) string {
	kw := strings.ToLower(keyword)
	own := map[string]bool{}
	for _, w := range offerWords(kw) {
		own[w] = true
	}
	for _, w := range offerWords(strings.ReplaceAll(strings.ToLower(item), kw, " ")) {
		if !own[w] {
			return item
		}
	}
	return notStated
}

func offerWords(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}
