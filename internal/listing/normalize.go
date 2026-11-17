package listing

import (
	"bytes"
	"html"
	"strings"

	"github.com/yuin/goldmark"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

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
	if err != nil || len(nodes) == 0 {
		return strings.TrimSpace(htmlToText(h))
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
// heading becomes its own h3, even when no blank line precedes or follows it.
func plainToHTML(plain string) string {
	hset := headingSet()
	lines := strings.Split(strings.ReplaceAll(plain, "\r\n", "\n"), "\n")

	type block struct {
		tag   string   // "ul", "h3", or "" (paragraph)
		lines []string // items for ul; single entry for h3 or paragraph
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
			blocks = append(blocks, block{tag: "h3", lines: []string{heading}})
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
		case "h3":
			buf.WriteString("<h3>")
			buf.WriteString(html.EscapeString(b.lines[0]))
			buf.WriteString("</h3>")
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
	out := strings.Join(flattenToBlocks(nodes), "")
	out = removeEmptyElements(out)
	out = promoteHeadings(out)
	return out
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
// loose inline content is wrapped in p.
func flattenToBlocks(nodes []*xhtml.Node) []string {
	var blocks []string
	var inlineAcc strings.Builder

	flushInline := func() {
		s := normalizeInlineContent(inlineAcc.String())
		inlineAcc.Reset()
		if s != "" {
			blocks = append(blocks, "<p>"+s+"</p>")
		}
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
					blocks = append(blocks, "<p>"+content+"</p>")
				}
			} else if a == atom.Ul || a == atom.Ol {
				flushInline()
				tag := n.Data
				var items []string
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == xhtml.ElementNode && c.DataAtom == atom.Li {
						content := normalizeInlineContent(renderInlineChildren(c))
						if content != "" {
							items = append(items, "<li>"+content+"</li>")
						}
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
// inline context are unwrapped.
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
			var b strings.Builder
			b.WriteByte('<')
			b.WriteString(n.Data)
			b.WriteByte('>')
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				b.WriteString(renderInlineNode(c))
			}
			b.WriteString("</")
			b.WriteString(n.Data)
			b.WriteByte('>')
			return b.String()
		}
		var b strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			b.WriteString(renderInlineNode(c))
		}
		return b.String()
	}
	return ""
}

// normalizeInlineContent normalises inline HTML content:
// collapses whitespace runs in text, trims edges, drops leading/trailing <br>,
// collapses runs of <br> to one.
func normalizeInlineContent(s string) string {
	s = collapseTextWhitespace(s)
	s = strings.TrimSpace(s)
	for strings.HasPrefix(s, "<br>") {
		s = strings.TrimSpace(s[4:])
	}
	for strings.HasSuffix(s, "<br>") {
		s = strings.TrimSpace(s[:len(s)-4])
	}
	return collapseBr(s)
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

// collapseBr collapses runs of <br> (with optional whitespace between) to one.
func collapseBr(s string) string {
	var b strings.Builder
	i, n := 0, len(s)
	for i < n {
		if strings.HasPrefix(s[i:], "<br>") {
			b.WriteString("<br>")
			i += 4
			for i < n {
				if strings.HasPrefix(s[i:], "<br>") {
					i += 4
				} else if s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r' {
					i++
				} else {
					break
				}
			}
		} else {
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

// removeEmptyElements removes elements whose content is empty or only <br>
// tags and whitespace, iterating until stable.
func removeEmptyElements(s string) string {
	tags := []string{"p", "h2", "h3", "h4", "li", "strong", "em", "ul", "ol"}
	changed := true
	for changed {
		changed = false
		for _, tag := range tags {
			open, close := "<"+tag+">", "</"+tag+">"
			for {
				start := strings.Index(s, open)
				if start < 0 {
					break
				}
				end := strings.Index(s[start:], close)
				if end < 0 {
					break
				}
				inner := s[start+len(open) : start+end]
				trimmed := strings.TrimSpace(strings.ReplaceAll(inner, "<br>", ""))
				if trimmed == "" {
					s = s[:start] + s[start+end+len(close):]
					changed = true
				} else {
					break
				}
			}
		}
	}
	return s
}

// promoteHeadings replaces a <p> whose entire text (stripped of optional
// strong/em wrappers and trailing colon) is a known section heading with <h3>.
func promoteHeadings(s string) string {
	hset := headingSet()
	var b strings.Builder
	remaining := s
	for {
		start := strings.Index(remaining, "<p>")
		if start < 0 {
			b.WriteString(remaining)
			break
		}
		end := strings.Index(remaining[start:], "</p>")
		if end < 0 {
			b.WriteString(remaining)
			break
		}
		b.WriteString(remaining[:start])
		inner := remaining[start+3 : start+end]
		remaining = remaining[start+end+4:]
		text := paragraphText(inner)
		if isHeading(text, hset) {
			b.WriteString("<h3>")
			b.WriteString(text)
			b.WriteString("</h3>")
		} else {
			b.WriteString("<p>")
			b.WriteString(inner)
			b.WriteString("</p>")
		}
	}
	return b.String()
}

// paragraphText returns the visible text of paragraph inner HTML, stripping
// strong/em wrappers, HTML-unescaping, and removing a trailing colon.
func paragraphText(inner string) string {
	text := strings.ReplaceAll(inner, "<strong>", "")
	text = strings.ReplaceAll(text, "</strong>", "")
	text = strings.ReplaceAll(text, "<em>", "")
	text = strings.ReplaceAll(text, "</em>", "")
	text = html.UnescapeString(text)
	text = strings.TrimSpace(text)
	text = strings.TrimSuffix(text, ":")
	return strings.TrimSpace(text)
}

// isHeading reports whether text (lowercased) is a known heading.
func isHeading(text string, hset map[string]bool) bool {
	return hset[strings.ToLower(text)]
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

// htmlToText is a fallback plain text extractor for strings that cannot be
// parsed as HTML fragments.
func htmlToText(h string) string {
	var b strings.Builder
	inTag := false
	for _, r := range h {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return b.String()
}
