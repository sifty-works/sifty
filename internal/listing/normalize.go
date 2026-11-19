package listing

import (
	"bytes"
	"html"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// FTSNorm normalises s for the full-text index: lowercases, replaces every run
// of non-letter/non-digit characters with a single space, and pads the result
// with a leading and trailing space. The padding ensures that even a one- or
// two-character term becomes a phrase of at least three characters when wrapped
// in its own spaces, so the trigram tokenizer can match it. An empty input
// returns a single space.
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

// Normalize converts raw vacancy text of the given Kind into sanitised HTML.
//
// Output is limited to h2 h3 h4 p ul ol li strong em br elements, with no
// attributes. Scripts, styles, HTML comments and their content are removed.
// Other tags are unwrapped.
//
// Heading mapping: h1 → h2; h5, h6 → h4. Container block elements (div,
// section, article, table rows/cells, etc.) become paragraph boundaries.
// Loose text at the top level or inside containers is wrapped in p.
//
// Whitespace rules: runs of whitespace inside text nodes are collapsed to one
// space; text is trimmed at block edges; whitespace-only text between blocks
// is removed; br at the start or end of a block is dropped.
//
// A paragraph whose entire text (bold or not, with or without a trailing
// colon) is a known section heading is promoted to h3. Empty elements are
// removed. The output is deterministic.
func Normalize(raw string, kind Kind) string {
	var htmlIn string
	switch kind {
	case HTML:
		htmlIn = raw
	case EscapedHTML:
		htmlIn = html.UnescapeString(raw)
	case Markdown:
		htmlIn = markdownToHTML(raw)
	case Plain:
		htmlIn = plainToHTML(raw)
	default:
		htmlIn = raw
	}
	return sanitise(htmlIn)
}

// PlainText returns the plain text of sanitised HTML. Block elements are
// separated by a single newline each. <br> becomes a newline. Never two
// blank lines in a row.
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

// markdownToHTML converts Markdown to HTML using goldmark.
func markdownToHTML(md string) string {
	var buf bytes.Buffer
	if err := goldmark.Convert([]byte(md), &buf); err != nil {
		return md
	}
	return buf.String()
}

// headingSet returns a lowercase lookup set for the known section headings.
func headingSet() map[string]bool {
	hset := make(map[string]bool, len(headings))
	for _, h := range headings {
		hset[strings.ToLower(h)] = true
	}
	return hset
}

// plainToHTML converts plain text to HTML.
// Blank lines separate paragraphs. Lines starting with -, * or • start list
// items. A line whose text (stripped of a trailing colon) is a known section
// heading becomes its own paragraph; sanitise will promote it to h3.
func plainToHTML(plain string) string {
	hset := headingSet()
	lines := strings.Split(strings.ReplaceAll(plain, "\r\n", "\n"), "\n")

	type block struct {
		tag   string   // "ul" or "" (paragraph)
		lines []string // items for ul; single entry for paragraph
	}

	var blocks []block
	var cur *block

	flush := func() {
		if cur != nil && len(cur.lines) > 0 {
			blocks = append(blocks, *cur)
		}
		cur = nil
	}

	isHeadingLine := func(s string) bool {
		s = strings.TrimSuffix(strings.TrimSpace(s), ":")
		return hset[strings.ToLower(strings.TrimSpace(s))]
	}

	for _, line := range lines {
		stripped := strings.TrimSpace(line)
		if stripped == "" {
			flush()
			continue
		}
		isBullet := strings.HasPrefix(stripped, "- ") ||
			strings.HasPrefix(stripped, "* ") ||
			strings.HasPrefix(stripped, "• ")
		if isBullet {
			if cur == nil || cur.tag != "ul" {
				flush()
				cur = &block{tag: "ul"}
			}
			text := strings.TrimPrefix(stripped, "- ")
			text = strings.TrimPrefix(text, "* ")
			text = strings.TrimPrefix(text, "• ")
			cur.lines = append(cur.lines, strings.TrimSpace(text))
		} else if isHeadingLine(stripped) {
			flush()
			heading := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(stripped), ":"))
			// Output as <p>; sanitise promotes it to <h3> via block flattening.
			blocks = append(blocks, block{tag: "", lines: []string{heading}})
		} else {
			if cur == nil {
				cur = &block{}
			} else if cur.tag != "" {
				flush()
				cur = &block{}
			}
			if len(cur.lines) > 0 {
				cur.lines[len(cur.lines)-1] += " " + stripped
			} else {
				cur.lines = append(cur.lines, stripped)
			}
		}
	}
	flush()

	var buf strings.Builder
	for _, b := range blocks {
		switch b.tag {
		case "ul":
			buf.WriteString("<ul>")
			for _, item := range b.lines {
				buf.WriteString("<li>")
				buf.WriteString(html.EscapeString(item))
				buf.WriteString("</li>")
			}
			buf.WriteString("</ul>")
		default:
			for _, para := range b.lines {
				buf.WriteString("<p>")
				buf.WriteString(html.EscapeString(para))
				buf.WriteString("</p>")
			}
		}
	}
	return buf.String()
}

// sanitise parses HTML and returns cleaned output.
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

// containerBlocks is the set of block container elements whose children are
// re-processed at top level (creating paragraph boundaries).
var containerBlocks = map[atom.Atom]bool{
	atom.Div: true, atom.Section: true, atom.Article: true,
	atom.Table: true, atom.Tbody: true, atom.Thead: true, atom.Tfoot: true,
	atom.Tr: true, atom.Td: true, atom.Th: true,
	atom.Blockquote: true, atom.Pre: true,
	atom.Dl: true, atom.Dt: true, atom.Dd: true,
	atom.Header: true, atom.Footer: true,
	atom.Main: true, atom.Nav: true, atom.Aside: true,
}

// mapHeadingTag maps a heading atom to its output tag. Returns "" otherwise.
// h1 → h2; h5, h6 → h4; h2, h3, h4 unchanged.
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

// flattenToBlocks processes a slice of nodes in block context and returns a
// slice of rendered block HTML strings. Container elements are flattened;
// loose inline content is wrapped in p. A paragraph whose full text is a
// known section heading is promoted to h3.
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

	// headingFromContent returns the plain-text heading if the paragraph
	// content (after stripping strong/em and a trailing colon) is a known
	// section heading; otherwise returns "".
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
			// skip
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

// renderInlineChildren renders all children of n as inline HTML.
func renderInlineChildren(n *xhtml.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(renderInlineNode(c))
	}
	return b.String()
}

// renderInlineNode renders a single node as inline HTML. Block elements in
// inline context are unwrapped. Empty strong and em elements are dropped.
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
				return "" // truly empty element
			}
			// If inner contains no visible text (only whitespace and/or <br>):
			noText := strings.TrimSpace(strings.ReplaceAll(inner, "<br>", ""))
			if noText == "" {
				if strings.Contains(inner, "<br>") {
					return "" // br-only content: drop element
				}
				return " " // whitespace-only text: replace element with one space
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

// collectListItems returns a flat slice of rendered inline strings, one per
// list item, for the given ul or ol node. Items inside a sublist (whether a
// direct child of this list or inside an li) are flattened into the same
// sequence; there is no nesting in the result.
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
			// A list directly inside a list (not inside an li): flatten its items.
			items = append(items, collectListItems(c)...)
		}
	}
	return items
}

// renderLiAsItems returns the content strings for a single li element.
// If the li contains a nested list, the li's own inline text (if any) becomes
// the first item and the nested items follow, all in the same slice.
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

// normalizeInlineContent normalises inline HTML content:
// collapses whitespace runs in text, trims edges, drops leading/trailing <br>,
// collapses consecutive <br> to one.
func normalizeInlineContent(s string) string {
	s = collapseTextWhitespace(s)
	s = strings.TrimSpace(s)
	for strings.HasPrefix(s, "<br>") {
		s = strings.TrimSpace(s[4:])
	}
	for strings.HasSuffix(s, "<br>") {
		s = strings.TrimSpace(s[:len(s)-4])
	}
	// Collapse consecutive <br> and strip space immediately after any <br>.
	prev := ""
	for s != prev {
		prev = s
		s = strings.ReplaceAll(s, "<br> <br>", "<br>")
		s = strings.ReplaceAll(s, "<br><br>", "<br>")
		s = strings.ReplaceAll(s, "<br> ", "<br>")
	}
	return s
}

// collapseTextWhitespace collapses runs of ASCII whitespace in the text
// portions of an HTML string (outside tag delimiters) to a single space.
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

// extractText walks a node tree and appends plain text to b, with a newline
// after each block-level element. <br> becomes a newline.
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
