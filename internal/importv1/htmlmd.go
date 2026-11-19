package importv1

import (
	"strings"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func htmlToMarkdown(h string) string {
	if strings.TrimSpace(h) == "" {
		return ""
	}
	nodes, err := xhtml.ParseFragment(strings.NewReader(h), &xhtml.Node{
		Type:     xhtml.ElementNode,
		Data:     "div",
		DataAtom: atom.Div,
	})
	if err != nil || len(nodes) == 0 {
		return strings.TrimSpace(h)
	}
	var sb strings.Builder
	for _, n := range nodes {
		renderMDNode(n, &sb, 0, false)
	}
	out := strings.TrimSpace(sb.String())
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	return out
}

func renderMDNode(n *xhtml.Node, sb *strings.Builder, listDepth int, inInline bool) {
	switch n.Type {
	case xhtml.TextNode:
		sb.WriteString(n.Data)
		return
	case xhtml.ElementNode:
	default:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			renderMDNode(c, sb, listDepth, inInline)
		}
		return
	}

	a := n.DataAtom
	switch a {
	case atom.P:
		ensureBlankLine(sb)
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			renderMDNode(c, sb, listDepth, true)
		}
		sb.WriteByte('\n')

	case atom.Br:
		sb.WriteString("  \n")

	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		level := 1
		switch a {
		case atom.H2:
			level = 2
		case atom.H3:
			level = 3
		case atom.H4, atom.H5, atom.H6:
			level = 4
		}
		ensureBlankLine(sb)
		sb.WriteString(strings.Repeat("#", level))
		sb.WriteByte(' ')
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			renderMDNode(c, sb, listDepth, true)
		}
		sb.WriteByte('\n')

	case atom.Strong, atom.B:
		sb.WriteString("**")
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			renderMDNode(c, sb, listDepth, inInline)
		}
		sb.WriteString("**")

	case atom.Em, atom.I:
		sb.WriteByte('_')
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			renderMDNode(c, sb, listDepth, inInline)
		}
		sb.WriteByte('_')

	case atom.A:
		href := attrVal(n, "href")
		if href == "" {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderMDNode(c, sb, listDepth, inInline)
			}
		} else {
			sb.WriteByte('[')
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderMDNode(c, sb, listDepth, inInline)
			}
			sb.WriteString("](")
			sb.WriteString(href)
			sb.WriteByte(')')
		}

	case atom.Ul, atom.Ol:
		ensureNewLine(sb)
		idx := 0
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == xhtml.ElementNode && c.DataAtom == atom.Li {
				idx++
				indent := strings.Repeat("  ", listDepth)
				if a == atom.Ol {
					sb.WriteString(indent)
					sb.WriteString("- ")
				} else {
					sb.WriteString(indent)
					sb.WriteString("- ")
				}
				for lc := c.FirstChild; lc != nil; lc = lc.NextSibling {
					renderMDNode(lc, sb, listDepth+1, true)
				}
				ensureNewLine(sb)
			}
		}
		if listDepth == 0 {
			sb.WriteByte('\n')
		}

	default:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			renderMDNode(c, sb, listDepth, inInline)
		}
	}
}

func ensureBlankLine(sb *strings.Builder) {
	s := sb.String()
	if s == "" {
		return
	}
	if strings.HasSuffix(s, "\n\n") {
		return
	}
	if strings.HasSuffix(s, "\n") {
		sb.WriteByte('\n')
		return
	}
	sb.WriteString("\n\n")
}

func ensureNewLine(sb *strings.Builder) {
	s := sb.String()
	if s == "" || strings.HasSuffix(s, "\n") {
		return
	}
	sb.WriteByte('\n')
}

func attrVal(n *xhtml.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
