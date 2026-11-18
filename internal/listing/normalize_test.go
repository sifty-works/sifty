package listing_test

import (
	"strings"
	"testing"

	"github.com/sifty-works/sifty-server/internal/listing"
)

// --- Normalize HTML ---

func TestNormalize_HTML(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"keeps allowed tags", `<p>Hello <strong>world</strong></p>`, `<p>Hello <strong>world</strong></p>`},
		{"strips attributes", `<p class="x" style="color:red">Text</p>`, `<p>Text</p>`},
		{"drops script", `<p>Before</p><script>alert("x")</script><p>After</p>`, `<p>Before</p><p>After</p>`},
		{"drops style", `<p>Before</p><style>.x{color:red}</style><p>After</p>`, `<p>Before</p><p>After</p>`},
		{"drops comments", `<p>A</p><!-- a comment --><p>B</p>`, `<p>A</p><p>B</p>`},
		{"unwraps disallowed tags", `<p><span class="x">hello</span></p>`, `<p>hello</p>`},
		{"escapes text", `<p>5 &lt; 10 &amp; &gt; 0</p>`, `<p>5 &lt; 10 &amp; &gt; 0</p>`},
		{"collapses consecutive br", `<p>A<br><br><br>B</p>`, `<p>A<br>B</p>`},
		{"promotes known heading", `<p>Responsibilities</p><p>Do things.</p>`, `<h3>Responsibilities</h3><p>Do things.</p>`},
		{"promotes heading with trailing colon", `<p>Benefits:</p>`, `<h3>Benefits</h3>`},
		{"promotes heading in strong", `<p><strong>Requirements</strong></p>`, `<h3>Requirements</h3>`},
		{"does not promote partial heading", `<p>Some responsibilities here</p>`, `<p>Some responsibilities here</p>`},
		{"case-insensitive heading", `<p>ABOUT THE ROLE</p>`, `<h3>ABOUT THE ROLE</h3>`},
		{"h1 becomes h2", `<h1>Title</h1><p>text</p>`, `<h2>Title</h2><p>text</p>`},
		{"h5 becomes h4", `<h5>Sub</h5>`, `<h4>Sub</h4>`},
		{"h6 becomes h4", `<h6>Also</h6>`, `<h4>Also</h4>`},
		{"div becomes paragraph boundary", `<div><p>foo</p><p>bar</p></div>`, `<p>foo</p><p>bar</p>`},
		{"loose text in div wrapped in p", `<div>text</div>`, `<p>text</p>`},
		{"empty li after unwrapping removed", `<ul><li><span></span></li><li>good</li></ul>`, `<ul><li>good</li></ul>`},
		{"empty strong removed", `<p><strong></strong>text</p>`, `<p>text</p>`},
		{"empty em removed", `<p>text<em></em></p>`, `<p>text</p>`},
		{"whitespace-only strong becomes space", `<p>a<strong> </strong>b</p>`, `<p>a b</p>`},
		{"em holding only br dropped", `<p><em><br></em><em>Text</em></p>`, `<p><em>Text</em></p>`},
		{"em holding only br at end of li dropped", `<ul><li>text.<em><br></em></li></ul>`, `<ul><li>text.</li></ul>`},
		{"space after br is stripped", `<p>text<br> Next</p>`, `<p>text<br>Next</p>`},
		{"whitespace-only em at list item end trimmed", `<ul><li>done. <em> </em></li></ul>`, `<ul><li>done.</li></ul>`},
		{"br-only paragraph removed", `<p><br></p>`, ``},
		{"br at block start dropped", `<p><br>text</p>`, `<p>text</p>`},
		{"br at block end dropped", `<p>text<br></p>`, `<p>text</p>`},
		{"br between blocks dropped", `<p>a</p><br><p>b</p>`, `<p>a</p><p>b</p>`},
		{"whitespace collapsed inside text", `<p>  foo   bar  </p>`, `<p>foo bar</p>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := listing.Normalize(tc.in, listing.HTML); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalize_HTMLDeterministic(t *testing.T) {
	in := `<p>Hello <strong>world</strong></p><ul><li>a</li><li>b</li></ul>`
	a := listing.Normalize(in, listing.HTML)
	b := listing.Normalize(in, listing.HTML)
	if a != b {
		t.Errorf("output is not deterministic: %q != %q", a, b)
	}
}

// --- Normalize EscapedHTML ---

func TestNormalize_EscapedHTML(t *testing.T) {
	in := `&lt;p&gt;Hello &lt;strong&gt;world&lt;/strong&gt;&lt;/p&gt;`
	got := listing.Normalize(in, listing.EscapedHTML)
	want := `<p>Hello <strong>world</strong></p>`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- Normalize Markdown ---

func TestNormalize_Markdown(t *testing.T) {
	in := "## Title\n\nSome *text* here.\n\n- Item one\n- Item two\n"
	got := listing.Normalize(in, listing.Markdown)
	if !strings.Contains(got, "<h2>") {
		t.Errorf("expected <h2> in output, got: %s", got)
	}
	if !strings.Contains(got, "<em>text</em>") {
		t.Errorf("expected <em>text</em> in output, got: %s", got)
	}
	if !strings.Contains(got, "<li>") {
		t.Errorf("expected <li> in output, got: %s", got)
	}
}

// --- Normalize Plain ---

func TestNormalize_Plain(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"paragraphs separated by blank lines",
			"First paragraph.\n\nSecond paragraph.",
			`<p>First paragraph.</p><p>Second paragraph.</p>`,
		},
		{
			"bullet lines become ul",
			"- Item one\n- Item two\n- Item three",
			`<ul><li>Item one</li><li>Item two</li><li>Item three</li></ul>`,
		},
		{
			"bullet variants * and bullet",
			"* Alpha\n• Beta",
			`<ul><li>Alpha</li><li>Beta</li></ul>`,
		},
		{
			"heading line without blank separator becomes h3",
			"Requirements\nMust have Go experience.",
			`<h3>Requirements</h3><p>Must have Go experience.</p>`,
		},
		{
			"heading line with trailing colon becomes h3 without colon",
			"Benefits:\nCompetitive pay.",
			`<h3>Benefits</h3><p>Competitive pay.</p>`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := listing.Normalize(tc.in, listing.Plain); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// --- PlainText ---

func TestPlainText(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantAll []string
	}{
		{
			"extracts paragraphs",
			`<p>First</p><p>Second</p>`,
			[]string{"First", "Second"},
		},
		{
			"extracts list items",
			`<ul><li>Apple</li><li>Banana</li></ul>`,
			[]string{"Apple", "Banana"},
		},
		{
			"br becomes newline",
			`<p>line1<br>line2</p>`,
			[]string{"line1\nline2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := listing.PlainText(tc.in)
			for _, want := range tc.wantAll {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in output %q", want, got)
				}
			}
		})
	}
}

func TestPlainText_NeverTwoBlankLines(t *testing.T) {
	in := `<ul><li>a</li></ul><ul><li>b</li></ul><p>c</p>`
	got := listing.PlainText(in)
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("got two blank lines in a row: %q", got)
	}
}
