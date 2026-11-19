package listing

import (
	"html"
	"strings"
	"unicode"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func FTSNorm(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.WriteByte(' ')
	inSpace := true
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			inSpace = false
		} else if !inSpace {
			b.WriteByte(' ')
			inSpace = true
		}
	}
	if !inSpace {
		b.WriteByte(' ')
	}
	return b.String()
}

func Normalize(raw string) string {
	return sanitise(raw)
}

func PlainText(h string) string {
	nodes, err := xhtml.ParseFragment(strings.NewReader(h), &xhtml.Node{
		Type:     xhtml.ElementNode,
		Data:     "div",
		DataAtom: atom.Div,
	})
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, n := range nodes {
		extractText(n, &b)
	}
	text := strings.TrimSpace(b.String())
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}
	return text
}

func headingSet() map[string]bool {
	hset := make(map[string]bool, len(headings))
	for _, h := range headings {
		hset[strings.ToLower(h)] = true
	}
	return hset
}

func sanitise(htmlIn string) string {
	nodes, err := xhtml.ParseFragment(strings.NewReader(htmlIn), &xhtml.Node{
		Type:     xhtml.ElementNode,
		Data:     "body",
		DataAtom: atom.Body,
	})
	if err != nil {
		return ""
	}
	return strings.Join(flattenToBlocks(nodes), "")
}

var containerBlocks = map[atom.Atom]bool{
	atom.Div: true, atom.Section: true, atom.Article: true,
	atom.Table: true, atom.Tbody: true, atom.Thead: true, atom.Tfoot: true,
	atom.Tr: true, atom.Td: true, atom.Th: true,
	atom.Blockquote: true, atom.Pre: true,
	atom.Dl: true, atom.Dt: true, atom.Dd: true,
	atom.Header: true, atom.Footer: true,
	atom.Main: true, atom.Nav: true, atom.Aside: true,
}

func mapHeadingTag(a atom.Atom) string {
	switch a {
	case atom.H1, atom.H2:
		return "h2"
	case atom.H3:
		return "h3"
	case atom.H4, atom.H5, atom.H6:
		return "h4"
	}
	return ""
}

func flattenToBlocks(nodes []*xhtml.Node) []string {
	hset := headingSet()
	var blocks []string
	var inlineAcc strings.Builder

	flushInline := func() {
		s := normalizeInlineContent(inlineAcc.String())
		inlineAcc.Reset()
		if s != "" {
			blocks = append(blocks, "<p>"+s+"</p>")
		}
	}

	headingFromContent := func(content string) string {
		t := strings.ReplaceAll(content, "<strong>", "")
		t = strings.ReplaceAll(t, "</strong>", "")
		t = strings.ReplaceAll(t, "<em>", "")
		t = strings.ReplaceAll(t, "</em>", "")
		t = html.UnescapeString(t)
		t = strings.TrimSpace(t)
		t = strings.TrimSuffix(t, ":")
		t = strings.TrimSpace(t)
		if t != "" && hset[strings.ToLower(t)] {
			return t
		}
		return ""
	}

	for _, n := range nodes {
		switch n.Type {
		case xhtml.TextNode:
			if strings.TrimSpace(n.Data) != "" {
				inlineAcc.WriteString(html.EscapeString(n.Data))
			}
		case xhtml.CommentNode, xhtml.DoctypeNode:
		case xhtml.ElementNode:
			a := n.DataAtom
			if a == atom.Script || a == atom.Style {
				continue
			}
			if heading := mapHeadingTag(a); heading != "" {
				flushInline()
				content := normalizeInlineContent(renderInlineChildren(n))
				if content != "" {
					blocks = append(blocks, "<"+heading+">"+content+"</"+heading+">")
				}
			} else if a == atom.P {
				flushInline()
				content := normalizeInlineContent(renderInlineChildren(n))
				if content != "" {
					if h := headingFromContent(content); h != "" {
						blocks = append(blocks, "<h3>"+html.EscapeString(h)+"</h3>")
					} else {
						blocks = append(blocks, "<p>"+content+"</p>")
					}
				}
			} else if a == atom.Ul || a == atom.Ol {
				flushInline()
				tag := n.Data
				rawItems := collectListItems(n)
				var items []string
				for _, item := range rawItems {
					if item != "" {
						items = append(items, "<li>"+item+"</li>")
					}
				}
				if len(items) > 0 {
					blocks = append(blocks, "<"+tag+">"+strings.Join(items, "")+"</"+tag+">")
				}
			} else if containerBlocks[a] {
				flushInline()
				var children []*xhtml.Node
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					children = append(children, c)
				}
				blocks = append(blocks, flattenToBlocks(children)...)
			} else {
				inlineAcc.WriteString(renderInlineNode(n))
			}
		case xhtml.DocumentNode:
			var children []*xhtml.Node
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				children = append(children, c)
			}
			blocks = append(blocks, flattenToBlocks(children)...)
		}
	}

	flushInline()
	return blocks
}

func renderInlineChildren(n *xhtml.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(renderInlineNode(c))
	}
	return b.String()
}

func renderInlineNode(n *xhtml.Node) string {
	switch n.Type {
	case xhtml.TextNode:
		return html.EscapeString(n.Data)
	case xhtml.ElementNode:
		a := n.DataAtom
		if a == atom.Script || a == atom.Style {
			return ""
		}
		if a == atom.Br {
			return "<br>"
		}
		if a == atom.Strong || a == atom.Em {
			inner := renderInlineChildren(n)
			if inner == "" {
				return ""
			}
			noText := strings.TrimSpace(strings.ReplaceAll(inner, "<br>", ""))
			if noText == "" {
				if strings.Contains(inner, "<br>") {
					return ""
				}
				return " "
			}
			return "<" + n.Data + ">" + inner + "</" + n.Data + ">"
		}
		var b strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			b.WriteString(renderInlineNode(c))
		}
		return b.String()
	}
	return ""
}

func collectListItems(listNode *xhtml.Node) []string {
	var items []string
	for c := listNode.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != xhtml.ElementNode {
			continue
		}
		switch c.DataAtom {
		case atom.Li:
			items = append(items, renderLiAsItems(c)...)
		case atom.Ul, atom.Ol:
			items = append(items, collectListItems(c)...)
		}
	}
	return items
}

func renderLiAsItems(liNode *xhtml.Node) []string {
	var inlineB strings.Builder
	var nested []string

	for c := liNode.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == xhtml.ElementNode && (c.DataAtom == atom.Ul || c.DataAtom == atom.Ol) {
			nested = append(nested, collectListItems(c)...)
		} else {
			inlineB.WriteString(renderInlineNode(c))
		}
	}

	var items []string
	text := normalizeInlineContent(inlineB.String())
	if text != "" {
		items = append(items, text)
	}
	items = append(items, nested...)
	return items
}

func normalizeInlineContent(s string) string {
	s = collapseTextWhitespace(s)
	s = strings.TrimSpace(s)
	for strings.HasPrefix(s, "<br>") {
		s = strings.TrimSpace(s[4:])
	}
	for strings.HasSuffix(s, "<br>") {
		s = strings.TrimSpace(s[:len(s)-4])
	}
	prev := ""
	for s != prev {
		prev = s
		s = strings.ReplaceAll(s, "<br> <br>", "<br>")
		s = strings.ReplaceAll(s, "<br><br>", "<br>")
		s = strings.ReplaceAll(s, "<br> ", "<br>")
	}
	return s
}

func collapseTextWhitespace(s string) string {
	var b strings.Builder
	inTag, prevSpace := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '<':
			inTag, prevSpace = true, false
			b.WriteByte(c)
		case c == '>':
			inTag = false
			b.WriteByte(c)
		case inTag:
			b.WriteByte(c)
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		default:
			prevSpace = false
			b.WriteByte(c)
		}
	}
	return b.String()
}

func extractText(n *xhtml.Node, b *strings.Builder) {
	if n == nil {
		return
	}
	switch n.Type {
	case xhtml.TextNode:
		b.WriteString(n.Data)
	case xhtml.ElementNode:
		if n.DataAtom == atom.Br {
			b.WriteByte('\n')
			return
		}
		isBlock := n.DataAtom == atom.P ||
			n.DataAtom == atom.H2 || n.DataAtom == atom.H3 || n.DataAtom == atom.H4 ||
			n.DataAtom == atom.Li || n.DataAtom == atom.Ul || n.DataAtom == atom.Ol
		if isBlock && b.Len() > 0 {
			s := b.String()
			if s[len(s)-1] != '\n' {
				b.WriteByte('\n')
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			extractText(c, b)
		}
		if isBlock {
			b.WriteByte('\n')
		}
	}
}
