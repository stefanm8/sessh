package confluence

import (
	"reflect"
	"strings"
	"testing"
)

// md renders storage format with only the site configured — the common case.
func md(storage string) string {
	return ToMarkdown(storage, MarkdownOptions{Site: site})
}

// mdWith renders with extra context (space, page id, resolved users).
func mdWith(storage string, opts MarkdownOptions) string {
	if opts.Site == "" {
		opts.Site = site
	}
	return ToMarkdown(storage, opts)
}

// ---------------------------------------------------------------------------
// The XHTML parser
// ---------------------------------------------------------------------------

func TestParseHTML(t *testing.T) {
	t.Run("elements nest and keep their attributes", func(t *testing.T) {
		root := ParseHTML(`<p class="x">a<strong>b</strong></p>`)
		if len(root.Children) != 1 {
			t.Fatalf("root has %d children, want 1", len(root.Children))
		}
		p := root.Children[0]
		if p.Name != "p" {
			t.Errorf("name = %q, want p", p.Name)
		}
		if p.Attr("class") != "x" {
			t.Errorf("class = %q, want x", p.Attr("class"))
		}
		if len(p.Children) != 2 {
			t.Errorf("p has %d children, want 2", len(p.Children))
		}
		if got := TextOf(p); got != "ab" {
			t.Errorf("TextOf = %q, want ab", got)
		}
	})

	t.Run("namespaced tags and attributes survive", func(t *testing.T) {
		root := ParseHTML(`<ac:structured-macro ac:name="code">` +
			`<ac:parameter ac:name="language">js</ac:parameter></ac:structured-macro>`)
		m := root.Children[0]
		if m.Name != "ac:structured-macro" {
			t.Fatalf("name = %q", m.Name)
		}
		if m.Attr("ac:name") != "code" {
			t.Errorf("ac:name = %q, want code", m.Attr("ac:name"))
		}
		if got := m.Children[0].Attr("ac:name"); got != "language" {
			t.Errorf("parameter ac:name = %q, want language", got)
		}
	})

	t.Run("tag names are lower-cased", func(t *testing.T) {
		root := ParseHTML(`<DIV><P>x</P></DIV>`)
		if root.Children[0].Name != "div" {
			t.Errorf("name = %q, want div", root.Children[0].Name)
		}
		if root.Children[0].Children[0].Name != "p" {
			t.Errorf("name = %q, want p", root.Children[0].Children[0].Name)
		}
	})

	t.Run("CDATA is kept verbatim, entities and all", func(t *testing.T) {
		root := ParseHTML(`<ac:plain-text-body><![CDATA[if (a < b && c) { }]]></ac:plain-text-body>`)
		if got := TextOf(root); got != "if (a < b && c) { }" {
			t.Errorf("TextOf = %q", got)
		}
	})

	t.Run("an unterminated CDATA section runs to the end of the input", func(t *testing.T) {
		root := ParseHTML(`<pre><![CDATA[tail & <b>`)
		if got := TextOf(root); got != "tail & <b>" {
			t.Errorf("TextOf = %q, want the raw tail", got)
		}
	})

	t.Run("self-closing and void elements do not swallow their siblings", func(t *testing.T) {
		root := ParseHTML(`<p>a<br/>b</p><hr/><p>c</p>`)
		if len(root.Children) != 3 {
			t.Fatalf("root has %d children, want 3", len(root.Children))
		}
		if got := TextOf(root.Children[0]); got != "ab" {
			t.Errorf("first child text = %q, want ab", got)
		}
		if got := TextOf(root.Children[2]); got != "c" {
			t.Errorf("third child text = %q, want c", got)
		}
	})

	t.Run("a void element without a slash still closes itself", func(t *testing.T) {
		root := ParseHTML(`<p>a<br>b</p><p>c</p>`)
		if len(root.Children) != 2 {
			t.Fatalf("root has %d children, want 2", len(root.Children))
		}
	})

	t.Run("unclosed tags close at the end of their parent", func(t *testing.T) {
		root := ParseHTML(`<div><p>a<p>b</div>`)
		if got := TextOf(root); got != "ab" {
			t.Errorf("TextOf = %q, want ab", got)
		}
		if len(root.Children) != 1 || root.Children[0].Name != "div" {
			t.Errorf("the closing </div> should have unwound everything inside it, got %d root children",
				len(root.Children))
		}
	})

	t.Run("a tag left open at the end of the document keeps its content", func(t *testing.T) {
		if got := TextOf(ParseHTML(`<p>a<em>b`)); got != "ab" {
			t.Errorf("TextOf = %q, want ab", got)
		}
	})

	t.Run("stray closing tags are ignored", func(t *testing.T) {
		if got := TextOf(ParseHTML(`a</span>b`)); got != "ab" {
			t.Errorf("TextOf = %q, want ab", got)
		}
	})

	t.Run("comments are dropped", func(t *testing.T) {
		if got := TextOf(ParseHTML(`a<!-- note -->b`)); got != "ab" {
			t.Errorf("TextOf = %q, want ab", got)
		}
	})

	t.Run("an unterminated comment eats the rest of the document", func(t *testing.T) {
		if got := TextOf(ParseHTML(`a<!-- note b`)); got != "a" {
			t.Errorf("TextOf = %q, want a", got)
		}
	})

	t.Run("doctypes and processing instructions are dropped", func(t *testing.T) {
		if got := TextOf(ParseHTML(`<?xml version="1.0"?><!DOCTYPE html>text`)); got != "text" {
			t.Errorf("TextOf = %q, want text", got)
		}
	})

	t.Run("attribute values may be double, single or unquoted", func(t *testing.T) {
		root := ParseHTML(`<a href="d" title='s' rel=noquote data-x=1>x</a>`)
		a := root.Children[0]
		for _, tc := range []struct{ key, want string }{
			{"href", "d"}, {"title", "s"}, {"rel", "noquote"}, {"data-x", "1"},
		} {
			if got := a.Attr(tc.key); got != tc.want {
				t.Errorf("%s = %q, want %q", tc.key, got, tc.want)
			}
		}
	})

	t.Run("a valueless attribute takes its own name as the value", func(t *testing.T) {
		root := ParseHTML(`<input disabled>`)
		if got := root.Children[0].Attr("disabled"); got != "disabled" {
			t.Errorf("disabled = %q, want %q", got, "disabled")
		}
	})

	t.Run("attribute names are lower-cased", func(t *testing.T) {
		root := ParseHTML(`<a HREF="x">y</a>`)
		if got := root.Children[0].Attr("href"); got != "x" {
			t.Errorf("href = %q, want x", got)
		}
	})

	t.Run("entities inside attribute values are decoded", func(t *testing.T) {
		root := ParseHTML(`<a href="/a?b=1&amp;c=2">x</a>`)
		if got := root.Children[0].Attr("href"); got != "/a?b=1&c=2" {
			t.Errorf("href = %q", got)
		}
	})

	t.Run("an unterminated quoted attribute takes the rest of the input", func(t *testing.T) {
		root := ParseHTML(`<a href="oops`)
		if got := root.Children[0].Attr("href"); got != "oops" {
			t.Errorf("href = %q, want oops", got)
		}
	})

	t.Run("a bare less-than in text is kept", func(t *testing.T) {
		if got := TextOf(ParseHTML(`a < b`)); got != "a < b" {
			t.Errorf("TextOf = %q, want %q", got, "a < b")
		}
	})

	t.Run("text entities are decoded on the way in", func(t *testing.T) {
		if got := TextOf(ParseHTML(`<p>a &amp; b &rarr; c</p>`)); got != "a & b → c" {
			t.Errorf("TextOf = %q", got)
		}
	})

	t.Run("empty input yields an empty root", func(t *testing.T) {
		root := ParseHTML("")
		if root == nil || root.Name != "#root" || len(root.Children) != 0 {
			t.Errorf("ParseHTML(\"\") = %+v, want an empty #root", root)
		}
	})
}

func TestNodeHelpers(t *testing.T) {
	root := ParseHTML(`<ul><li>a</li><li>b</li><p>p</p></ul>`)
	ul := root.Children[0]

	t.Run("FindChild returns the first immediate match", func(t *testing.T) {
		li := FindChild(ul, "li")
		if li == nil || TextOf(li) != "a" {
			t.Errorf("FindChild(li) = %v", li)
		}
		if got := FindChild(ul, "table"); got != nil {
			t.Errorf("FindChild(table) = %v, want nil", got)
		}
		if got := FindChild(nil, "li"); got != nil {
			t.Errorf("FindChild(nil) = %v, want nil", got)
		}
	})

	t.Run("ChildrenNamed returns only immediate matches", func(t *testing.T) {
		got := ChildrenNamed(ul, "li")
		if len(got) != 2 {
			t.Fatalf("ChildrenNamed(li) returned %d, want 2", len(got))
		}
		if ChildrenNamed(nil, "li") != nil {
			t.Error("ChildrenNamed(nil) should be nil")
		}
	})

	t.Run("IsElement distinguishes text from elements", func(t *testing.T) {
		if IsElement(nil) {
			t.Error("IsElement(nil) = true")
		}
		if !IsElement(ul, "ul") {
			t.Error("IsElement(ul, ul) = false")
		}
		if IsElement(ul, "ol") {
			t.Error("IsElement(ul, ol) = true")
		}
		text := FindChild(ul, "li").Children[0]
		if IsElement(text) {
			t.Error("IsElement(text node) = true")
		}
	})

	t.Run("Attr on a nil node or missing key is empty", func(t *testing.T) {
		var n *Node
		if n.Attr("x") != "" {
			t.Error("nil.Attr should be empty")
		}
		if ul.Attr("nope") != "" {
			t.Error("missing attr should be empty")
		}
	})

	t.Run("TextOf(nil) is empty", func(t *testing.T) {
		if TextOf(nil) != "" {
			t.Error("TextOf(nil) should be empty")
		}
	})
}

func TestDecodeEntities(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"nothing to decode takes the fast path", "plain text", "plain text"},
		{"the core five", "a&amp;b &lt;c&gt; &quot;d&quot; &apos;e&apos;", `a&b <c> "d" 'e'`},
		{"decimal numeric references", "&#39;d&#39;", "'d'"},
		{"hex numeric references", "&#x2014;", "—"},
		{"upper-case hex marker", "&#X2014;", "—"},
		{"typography and arrows", "&ndash; &mdash; &hellip; &rarr;", "– — … →"},
		{"a non-breaking space becomes a plain space", "a&nbsp;b", "a b"},
		{"a mixed run", "a&amp;b &lt;c&gt; &#39;d&#39; &#x2014; &rarr;", "a&b <c> 'd' — →"},
		{"unknown entities are left verbatim", "&notreal; &amp;", "&notreal; &"},
		{"an ampersand with no semicolon is left alone", "a & b", "a & b"},
		{"a trailing ampersand is left alone", "a&", "a&"},
		{"double-encoding is only decoded one level", "&amp;amp;", "&amp;"},
		{"an over-long candidate is not an entity", "&" + strings.Repeat("x", 40) + ";", "&" + strings.Repeat("x", 40) + ";"},
		{"an invalid numeric reference is left alone", "&#xZZ;", "&#xZZ;"},
		{"a surrogate code point is rejected", "&#55296;", "&#55296;"},
		{"an out-of-range code point is rejected", "&#1114112;", "&#1114112;"},
		{"an empty entity name is left alone", "&;", "&;"},
		{"entity names are matched case-insensitively as a fallback", "&AMP;", "&"},
		{"empty input", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DecodeEntities(tt.in); got != tt.want {
				t.Errorf("DecodeEntities(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Classification and text helpers
// ---------------------------------------------------------------------------

func TestIsBlockNode(t *testing.T) {
	tests := []struct {
		name string
		html string
		want bool
	}{
		{"a paragraph is a block", `<p>x</p>`, true},
		{"a div is a block", `<div>x</div>`, true},
		{"a table row is a block", `<tr><td>x</td></tr>`, true},
		{"a span is inline", `<span>x</span>`, false},
		{"a link is inline", `<a href="x">y</a>`, false},
		{"br is inline, despite hr being a block", `<br/>`, false},
		{"an hr is a block", `<hr/>`, true},
		{"an unknown tag is inline", `<mystery>x</mystery>`, false},
		{"a new-editor layout is a block", `<ac:layout><p>x</p></ac:layout>`, true},
		{"a task list is a block", `<ac:task-list/>`, true},
		{"a code macro is a block", `<ac:structured-macro ac:name="code"/>`, true},
		{"an info macro is a block", `<ac:structured-macro ac:name="info"/>`, true},
		{"a status lozenge is inline so it cannot split its paragraph",
			`<ac:structured-macro ac:name="status"/>`, false},
		{"a mention is inline", `<ac:structured-macro ac:name="mention"/>`, false},
		{"a date macro is inline", `<ac:structured-macro ac:name="date"/>`, false},
		{"the macro name is matched case-insensitively",
			`<ac:structured-macro ac:name="STATUS"/>`, false},
		{"a jira macro with a key is a single inline issue link",
			`<ac:structured-macro ac:name="jira"><ac:parameter ac:name="key">ENG-1</ac:parameter></ac:structured-macro>`, false},
		{"a jira macro with a JQL query is a results table",
			`<ac:structured-macro ac:name="jira"><ac:parameter ac:name="jqlQuery">project = ENG</ac:parameter></ac:structured-macro>`, true},
		{"a bare jira macro is a block", `<ac:structured-macro ac:name="jira"/>`, true},
		{"the legacy ac:macro element is classified the same way",
			`<ac:macro ac:name="status"/>`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := ParseHTML(tt.html).Children[0]
			if got := isBlockNode(node); got != tt.want {
				t.Errorf("isBlockNode(%s) = %v, want %v", tt.html, got, tt.want)
			}
		})
	}

	t.Run("a text node is never a block", func(t *testing.T) {
		if isBlockNode(ParseHTML("bare text").Children[0]) {
			t.Error("isBlockNode(text) = true")
		}
	})
	t.Run("nil is never a block", func(t *testing.T) {
		if isBlockNode(nil) {
			t.Error("isBlockNode(nil) = true")
		}
	})
}

func TestTidy(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"blank runs collapse to one blank line", "a\n\n\n\nb", "a\n\nb"},
		{"a single blank line is preserved", "a\n\nb", "a\n\nb"},
		{"a two-space hard break survives", "a  \nb", "a  \nb"},
		{"a longer run of trailing spaces is clamped to a hard break", "a    \nb", "a  \nb"},
		{"a single trailing space is noise and is removed", "a \nb", "a\nb"},
		{"a trailing tab is noise and is removed", "a\t\nb", "a\nb"},
		{"CRLF is normalized", "a\r\nb", "a\nb"},
		{"a lone CR is normalized", "a\rb", "a\nb"},
		{"leading and trailing whitespace is trimmed", "\n\n  a  \n\n", "a"},
		{"empty stays empty", "", ""},
		{"whitespace only collapses to empty", "\n \n\t\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tidy(tt.in); got != tt.want {
				t.Errorf("tidy(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestEscapeText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"asterisks, brackets and angle brackets", "a * b [c] <d>", `a \* b \[c\] \<d>`},
		{"backticks", "back`tick", "back\\`tick"},
		{"backslashes", `a\b`, `a\\b`},
		{"a line-leading hash is a heading and must be escaped", "# heading", `\# heading`},
		{"a line-leading angle bracket is a quote and must be escaped", "> quote", `\> quote`},
		{"indentation before the marker is preserved", "  > quote", `  \> quote`},
		{"a mid-line hash is harmless", "a # b", "a # b"},
		{"a mid-line angle bracket is harmless", "a > b", "a > b"},
		{"every line is considered, not just the first", "a\n# b", "a\n\\# b"},
		{"snake_case survives unescaped so identifiers stay readable", "my_var_name", "my_var_name"},
		{"underscores are never escaped", "_leading and trailing_", "_leading and trailing_"},
		{"empty stays empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := escapeText(tt.in); got != tt.want {
				t.Errorf("escapeText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFence(t *testing.T) {
	tests := []struct {
		name string
		code string
		lang string
		want string
	}{
		{"a plain block", "x()", "", "```\nx()\n```"},
		{"a language hint is appended to the opening fence", "x()", "javascript", "```javascript\nx()\n```"},
		{"leading and trailing newlines are trimmed", "\n\nx()\n\n", "", "```\nx()\n```"},
		{"internal blank lines are preserved", "a\n\nb", "", "```\na\n\nb\n```"},
		{"indentation is preserved", "  indented", "", "```\n  indented\n```"},
		{"a body containing a fence gets a longer fence", "```\nx\n```", "", "````\n```\nx\n```\n````"},
		{"a body containing a longer fence escalates further", "````\nx\n````", "", "`````\n````\nx\n````\n`````"},
		{"an empty body still produces a fence", "", "", "```\n\n```"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fence(tt.code, tt.lang); got != tt.want {
				t.Errorf("fence(%q, %q) = %q, want %q", tt.code, tt.lang, got, tt.want)
			}
		})
	}
}

func TestMapLang(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"a known alias", "js", "javascript"},
		{"typescript", "ts", "typescript"},
		{"python", "py", "python"},
		{"shell", "sh", "bash"},
		{"yml", "yml", "yaml"},
		{"none means no hint at all", "none", ""},
		{"text means no hint", "text", ""},
		{"plaintext means no hint", "plaintext", ""},
		{"html/xml collapses to html", "html/xml", "html"},
		{"a name with a hash survives", "c#", "csharp"},
		{"c++ maps to cpp", "c++", "cpp"},
		{"case is normalized", "YAML", "yaml"},
		{"an unknown language passes through lower-cased", "rust", "rust"},
		{"surrounding whitespace is trimmed", "  Go  ", "go"},
		{"characters that would break a fence are stripped", "java script", "javascript"},
		{"a plus sign is allowed through", "notreal++", "notreal++"},
		{"empty stays empty", "", ""},
		{"whitespace only is empty", "   ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mapLang(tt.in); got != tt.want {
				t.Errorf("mapLang(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ToMarkdown — plain HTML
// ---------------------------------------------------------------------------

func TestToMarkdownBasics(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty input", "", ""},
		{"whitespace only", "   \n  ", ""},
		{"headings", "<h1>One</h1><h3>Three</h3>", "# One\n\n### Three"},
		{"all six heading levels", "<h6>Six</h6>", "###### Six"},
		{"an empty heading is dropped", "<h1>  </h1><p>x</p>", "x"},
		{"a heading with inline markup is flattened to one line",
			"<h2>A <em>b</em>\nc</h2>", "## A *b* c"},
		{
			name: "paragraphs, bold and italic",
			in:   "<p>Hello <strong>bold</strong> and <em>italic</em>.</p><p>Second.</p>",
			want: "Hello **bold** and *italic*.\n\nSecond.",
		},
		{"b and i are the same as strong and em", "<p><b>a</b> <i>b</i></p>", "**a** *b*"},
		{"strikethrough", "<p><del>gone</del> <s>also</s></p>", "~~gone~~ ~~also~~"},
		{"empty emphasis contributes nothing", "<p>a<strong> </strong>b</p>", "ab"},
		{
			name: "inline code and links",
			in:   `<p>Run <code>npm test</code> — see <a href="https://x.dev">docs</a>.</p>`,
			want: "Run `npm test` — see [docs](https://x.dev).",
		},
		{"code containing a backtick gets a longer delimiter",
			"<p><code>a `b` c</code></p>", "``a `b` c``"},
		{"code starting with a backtick is padded",
			"<p><code>`x</code></p>", "`` `x ``"},
		{"empty code contributes nothing", "<p>a<code> </code>b</p>", "ab"},
		{"tt is inline code too", "<p><tt>x</tt></p>", "`x`"},
		{
			name: "relative links are absolutised against the site",
			in:   `<p><a href="/wiki/spaces/X">X</a></p>`,
			want: "[X](" + site + "/wiki/spaces/X)",
		},
		{"mailto links are left alone", `<p><a href="mailto:a@b.c">mail</a></p>`, "[mail](mailto:a@b.c)"},
		{"fragment links are left alone", `<p><a href="#top">top</a></p>`, "[top](#top)"},
		{"a link with no label becomes an autolink", `<p><a href="https://x.dev"></a></p>`, "<https://x.dev>"},
		{"a link with no href degrades to its label", `<p><a>label</a></p>`, "label"},
		{"a multi-line link label is flattened", "<p><a href=\"https://x.dev\">a\n  b</a></p>", "[a b](https://x.dev)"},
		{"unordered list", "<ul><li>a</li><li>b</li></ul>", "- a\n- b"},
		{"ordered list", "<ol><li>a</li><li>b</li></ol>", "1. a\n2. b"},
		{"an ordered list honours its start attribute", `<ol start="3"><li>a</li><li>b</li></ol>`, "3. a\n4. b"},
		{
			name: "nested lists hang tightly under their parent item",
			in:   "<ul><li>a<ul><li>a1</li></ul></li><li>b</li></ul>",
			want: "- a\n  - a1\n- b",
		},
		{
			name: "nesting still hangs tight when the item text is wrapped in a paragraph",
			in:   "<ol><li><p>a</p><ul><li>a1</li></ul></li></ol>",
			want: "1. a\n   - a1",
		},
		{
			name: "a list item with two paragraphs keeps them separated and indented",
			in:   "<ul><li><p>one</p><p>two</p></li></ul>",
			want: "- one\n\n  two",
		},
		{"a list with no items still renders its content", "<ul>loose text</ul>", "loose text"},
		{"blockquote", "<blockquote><p>quoted</p></blockquote>", "> quoted"},
		{
			name: "a blockquote with two paragraphs keeps the marker on the blank line",
			in:   "<blockquote><p>a</p><p>b</p></blockquote>",
			want: "> a\n>\n> b",
		},
		{"horizontal rule", "<p>a</p><hr/><p>b</p>", "a\n\n---\n\nb"},
		{"pre/code becomes a fenced block", `<pre><code class="language-js">x()</code></pre>`, "```javascript\nx()\n```"},
		{"the brush- class convention is understood too", `<pre class="brush:python">x</pre>`, "```python\nx\n```"},
		{"a pre with no class has no language hint", "<pre>plain</pre>", "```\nplain\n```"},
		{"a definition list", "<dl><dt>Term</dt><dd>Definition</dd></dl>", "**Term**\n: Definition"},
		{"br outside a table is a Markdown hard break", "<p>a<br/>b</p>", "a  \nb"},
		{"an image", `<p><img src="https://x.dev/a.png" alt="Alt"/></p>`, "![Alt](https://x.dev/a.png)"},
		{"an image falls back to its title for alt text", `<p><img src="/a.png" title="T"/></p>`, "![T](" + site + "/a.png)"},
		{"an image with no source renders nothing", `<p><img alt="x"/></p>`, ""},
		{"a time element prefers its machine-readable datetime",
			`<p><time datetime="2026-08-04">Aug 4</time></p>`, "2026-08-04"},
		{"unknown elements are transparent", "<mystery><p>kept</p></mystery>", "kept"},
		{"details and summary are transparent",
			"<details><summary>S</summary><p>B</p></details>", "S\n\nB"},
		{
			name: "markdown metacharacters in text are escaped",
			in:   "<p>a * b [c] &lt;d&gt;</p>",
			want: `a \* b \[c\] \<d>`,
		},
		{"a line-leading angle bracket is escaped", "<p>&gt; not a quote</p>", `\> not a quote`},
		{"snake_case survives unescaped", "<p>my_var_name</p>", "my_var_name"},
		{"inline runs between blocks become their own paragraph",
			"<p>a</p>loose<p>b</p>", "a\n\nloose\n\nb"},
		{"blank blocks are dropped rather than leaving holes",
			"<p>a</p><p>   </p><p>b</p>", "a\n\nb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := md(tt.in); got != tt.want {
				t.Errorf("ToMarkdown(%q) =\n  %q\nwant\n  %q", tt.in, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tables
// ---------------------------------------------------------------------------

func TestToMarkdownTables(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "a th header row becomes the Markdown header",
			in:   "<table><tbody><tr><th>A</th><th>B</th></tr><tr><td>1</td><td>2</td></tr></tbody></table>",
			want: "| A | B |\n| --- | --- |\n| 1 | 2 |",
		},
		{
			name: "a thead/tbody split is flattened",
			in:   "<table><thead><tr><th>A</th><th>B</th></tr></thead><tbody><tr><td>1</td><td>2</td></tr></tbody></table>",
			want: "| A | B |\n| --- | --- |\n| 1 | 2 |",
		},
		{
			name: "a th first column is data, not a header row, so an empty header is synthesized",
			in:   "<table><tbody><tr><th>Status</th><td>Draft</td></tr></tbody></table>",
			want: "|  |  |\n| --- | --- |\n| Status | Draft |",
		},
		{
			name: "several th-first-column rows all stay data",
			in: "<table><tbody><tr><th>Status</th><td>Draft</td></tr>" +
				"<tr><th>Owner</th><td>Ana</td></tr></tbody></table>",
			want: "|  |  |\n| --- | --- |\n| Status | Draft |\n| Owner | Ana |",
		},
		{
			name: "a short all-th first row is not a header row either",
			in:   "<table><tbody><tr><th>A</th></tr><tr><td>1</td><td>2</td></tr></tbody></table>",
			want: "|  |  |\n| --- | --- |\n| A |  |\n| 1 | 2 |",
		},
		{
			name: "cell newlines become br and pipes are escaped",
			in:   "<table><tbody><tr><td><p>a</p><p>b|c</p></td></tr></tbody></table>",
			want: `|  |` + "\n" + `| --- |` + "\n" + `| a<br><br>b\|c |`,
		},
		{
			name: "a br inside a cell stays a br rather than a hard break",
			in:   "<table><tbody><tr><td>a<br/>b</td></tr></tbody></table>",
			want: "|  |\n| --- |\n| a<br>b |",
		},
		{
			name: "colspan pads the row so the columns line up",
			in: "<table><tbody><tr><th>A</th><th>B</th></tr>" +
				`<tr><td colspan="2">wide</td></tr></tbody></table>`,
			want: "| A | B |\n| --- | --- |\n| wide |  |",
		},
		{
			name: "a colspan of one is a no-op",
			in:   `<table><tbody><tr><td colspan="1">a</td><td>b</td></tr></tbody></table>`,
			want: "|  |  |\n| --- | --- |\n| a | b |",
		},
		{
			name: "an unparseable colspan is ignored",
			in:   `<table><tbody><tr><td colspan="lots">a</td></tr></tbody></table>`,
			want: "|  |\n| --- |\n| a |",
		},
		{
			name: "rows shorter than the widest are padded",
			in:   "<table><tbody><tr><th>A</th><th>B</th></tr><tr><td>1</td></tr></tbody></table>",
			want: "| A | B |\n| --- | --- |\n| 1 |  |",
		},
		{
			name: "non-cell children of a row are ignored",
			in:   "<table><tbody><tr>stray<td>a</td></tr></tbody></table>",
			want: "|  |\n| --- |\n| a |",
		},
		{
			name: "an empty table renders nothing",
			in:   "<table><tbody></tbody></table>",
			want: "",
		},
		{
			name: "a table whose only row has no cells renders nothing",
			in:   "<table><tbody><tr></tr></tbody></table>",
			want: "",
		},
		{
			name: "a link inside a cell survives",
			in:   `<table><tbody><tr><th>A</th></tr><tr><td><a href="https://x.dev">d</a></td></tr></tbody></table>`,
			want: "| A |\n| --- |\n| [d](https://x.dev) |",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := md(tt.in); got != tt.want {
				t.Errorf("ToMarkdown(%q) =\n  %q\nwant\n  %q", tt.in, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Confluence macros
// ---------------------------------------------------------------------------

func TestToMarkdownMacros(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "the code macro uses its language parameter and CDATA body",
			in: `<ac:structured-macro ac:name="code">` +
				`<ac:parameter ac:name="language">bash</ac:parameter>` +
				`<ac:plain-text-body><![CDATA[echo "hi" && ls]]></ac:plain-text-body>` +
				`</ac:structured-macro>`,
			want: "```bash\necho \"hi\" && ls\n```",
		},
		{
			name: "a code body containing a fence gets a longer fence",
			in: `<ac:structured-macro ac:name="code"><ac:plain-text-body>` +
				"<![CDATA[```\nx\n```]]></ac:plain-text-body></ac:structured-macro>",
			want: "````\n```\nx\n```\n````",
		},
		{
			name: "a code macro title is rendered above the block",
			in: `<ac:structured-macro ac:name="code">` +
				`<ac:parameter ac:name="title">Example</ac:parameter>` +
				`<ac:plain-text-body><![CDATA[x]]></ac:plain-text-body></ac:structured-macro>`,
			want: "**Example**\n\n```\nx\n```",
		},
		{
			name: "a code macro with no language has no hint",
			in: `<ac:structured-macro ac:name="code">` +
				`<ac:plain-text-body><![CDATA[x]]></ac:plain-text-body></ac:structured-macro>`,
			want: "```\nx\n```",
		},
		{
			name: "noformat is an unhinted fence",
			in: `<ac:structured-macro ac:name="noformat">` +
				`<ac:plain-text-body><![CDATA[raw   text]]></ac:plain-text-body></ac:structured-macro>`,
			want: "```\nraw   text\n```",
		},
		{
			name: "an info panel becomes a labelled blockquote",
			in: `<ac:structured-macro ac:name="info"><ac:parameter ac:name="title">Heads up</ac:parameter>` +
				`<ac:rich-text-body><p>Body text</p></ac:rich-text-body></ac:structured-macro>`,
			want: "> **Info: Heads up**\n>\n> Body text",
		},
		{
			name: "a warning panel without a title still gets its label",
			in:   `<ac:structured-macro ac:name="warning"><ac:rich-text-body><p>Careful</p></ac:rich-text-body></ac:structured-macro>`,
			want: "> **Warning**\n>\n> Careful",
		},
		{
			name: "a note panel",
			in:   `<ac:structured-macro ac:name="note"><ac:rich-text-body><p>Note this</p></ac:rich-text-body></ac:structured-macro>`,
			want: "> **Note**\n>\n> Note this",
		},
		{
			name: "a generic panel has no label, only its title",
			in: `<ac:structured-macro ac:name="panel"><ac:parameter ac:name="title">My Panel</ac:parameter>` +
				`<ac:rich-text-body><p>Body</p></ac:rich-text-body></ac:structured-macro>`,
			want: "> **My Panel**\n>\n> Body",
		},
		{
			name: "a generic panel with neither label nor title is a plain quote",
			in:   `<ac:structured-macro ac:name="panel"><ac:rich-text-body><p>Body</p></ac:rich-text-body></ac:structured-macro>`,
			want: "> Body",
		},
		{
			name: "an empty panel renders nothing",
			in:   `<ac:structured-macro ac:name="panel"><ac:rich-text-body></ac:rich-text-body></ac:structured-macro>`,
			want: "",
		},
		{
			name: "a panel with a label but no body is just the label",
			in:   `<ac:structured-macro ac:name="tip"/>`,
			want: "> **Tip**",
		},
		{
			name: "a multi-block panel keeps its structure inside the quote",
			in: `<ac:structured-macro ac:name="info"><ac:rich-text-body>` +
				`<p>One</p><ul><li>a</li></ul></ac:rich-text-body></ac:structured-macro>`,
			want: "> **Info**\n>\n> One\n>\n> - a",
		},
		{
			name: "a status lozenge stays inline inside its paragraph",
			in: `<p>State: <ac:structured-macro ac:name="status">` +
				`<ac:parameter ac:name="title">IN PROGRESS</ac:parameter></ac:structured-macro> today.</p>`,
			want: "State: `IN PROGRESS` today.",
		},
		{
			name: "a status lozenge with no title renders nothing",
			in:   `<p>State:<ac:structured-macro ac:name="status"/></p>`,
			want: "State:",
		},
		{
			name: "the expand macro becomes a details block",
			in: `<ac:structured-macro ac:name="expand"><ac:parameter ac:name="title">More</ac:parameter>` +
				`<ac:rich-text-body><p>Hidden</p></ac:rich-text-body></ac:structured-macro>`,
			want: "<details>\n<summary>More</summary>\n\nHidden\n\n</details>",
		},
		{
			name: "an untitled expand block gets a default summary",
			in:   `<ac:structured-macro ac:name="expand"><ac:rich-text-body><p>Hidden</p></ac:rich-text-body></ac:structured-macro>`,
			want: "<details>\n<summary>Details</summary>\n\nHidden\n\n</details>",
		},
		{
			name: "toc and other generated macros are dropped entirely",
			in:   `<p>a</p><ac:structured-macro ac:name="toc"/><p>b</p>`,
			want: "a\n\nb",
		},
		{
			name: "a children macro is dropped",
			in:   `<p>a</p><ac:structured-macro ac:name="children"/>`,
			want: "a",
		},
		{
			name: "an unknown macro leaves a named comment so the reader knows",
			in:   `<ac:structured-macro ac:name="mystery"/>`,
			want: "<!-- confluence macro: mystery -->",
		},
		{
			name: "an unknown macro's title is named in the comment",
			in:   `<ac:structured-macro ac:name="mystery"><ac:parameter ac:name="title">T</ac:parameter></ac:structured-macro>`,
			want: "<!-- confluence macro: mystery (T) -->",
		},
		{
			name: "an unknown macro with a rich body renders the body",
			in:   `<ac:structured-macro ac:name="mystery"><ac:rich-text-body><p>kept</p></ac:rich-text-body></ac:structured-macro>`,
			want: "kept",
		},
		{
			name: "an unknown macro with a plain body renders it as code",
			in:   `<ac:structured-macro ac:name="mystery"><ac:plain-text-body><![CDATA[raw]]></ac:plain-text-body></ac:structured-macro>`,
			want: "```\nraw\n```",
		},
		{
			name: "a jira macro with a key links to the issue",
			in:   `<p><ac:structured-macro ac:name="jira"><ac:parameter ac:name="key">ENG-1</ac:parameter></ac:structured-macro></p>`,
			want: "[ENG-1](" + site + "/browse/ENG-1)",
		},
		{
			name: "a jira macro with a JQL query leaves a comment",
			in: `<ac:structured-macro ac:name="jira">` +
				`<ac:parameter ac:name="jqlQuery">project = ENG</ac:parameter></ac:structured-macro>`,
			want: "<!-- Jira issues macro: project = ENG -->",
		},
		{
			name: "a bare jira macro still leaves a marker",
			in:   `<ac:structured-macro ac:name="jira"/>`,
			want: "<!-- Jira issues macro -->",
		},
		{
			name: "an include macro names what was included",
			in:   `<ac:structured-macro ac:name="include"><ac:parameter ac:name="page">Other Page</ac:parameter></ac:structured-macro>`,
			want: "<!-- included content: Other Page -->",
		},
		{
			name: "an include macro with no parameters still says something was there",
			in:   `<ac:structured-macro ac:name="include"/>`,
			want: "<!-- included content: another page -->",
		},
		{
			name: "layout macros are transparent",
			in: `<ac:structured-macro ac:name="section"><ac:rich-text-body>` +
				`<p>inside</p></ac:rich-text-body></ac:structured-macro>`,
			want: "inside",
		},
		{
			name: "the legacy ac:macro element is handled like ac:structured-macro",
			in:   `<ac:macro ac:name="code"><ac:plain-text-body><![CDATA[x]]></ac:plain-text-body></ac:macro>`,
			want: "```\nx\n```",
		},
		{
			name: "a task list becomes checkboxes",
			in: `<ac:task-list>` +
				`<ac:task><ac:task-status>complete</ac:task-status><ac:task-body>done thing</ac:task-body></ac:task>` +
				`<ac:task><ac:task-status>incomplete</ac:task-status><ac:task-body>todo thing</ac:task-body></ac:task>` +
				`</ac:task-list>`,
			want: "- [x] done thing\n- [ ] todo thing",
		},
		{
			name: "a task with no status is unchecked",
			in:   `<ac:task-list><ac:task><ac:task-body>thing</ac:task-body></ac:task></ac:task-list>`,
			want: "- [ ] thing",
		},
		{
			name: "task status matching is case- and space-insensitive",
			in: `<ac:task-list><ac:task><ac:task-status> COMPLETE </ac:task-status>` +
				`<ac:task-body>thing</ac:task-body></ac:task></ac:task-list>`,
			want: "- [x] thing",
		},
		{
			name: "task bodies keep their inline markup",
			in: `<ac:task-list><ac:task><ac:task-status>incomplete</ac:task-status>` +
				`<ac:task-body><strong>bold</strong> thing</ac:task-body></ac:task></ac:task-list>`,
			want: "- [ ] **bold** thing",
		},
		{
			name: "new-editor layouts are transparent",
			in: `<ac:layout><ac:layout-section ac:type="two_equal">` +
				`<ac:layout-cell><p>left</p></ac:layout-cell>` +
				`<ac:layout-cell><p>right</p></ac:layout-cell>` +
				`</ac:layout-section></ac:layout>`,
			want: "left\n\nright",
		},
		{
			name: "adf extensions prefer their human-readable fallback",
			in: `<ac:adf-extension><ac:adf-node type="panel">` +
				`<ac:adf-attribute key="panel-type">info</ac:adf-attribute>` +
				`</ac:adf-node><ac:adf-fallback><p>panel text</p></ac:adf-fallback></ac:adf-extension>`,
			want: "panel text",
		},
		{
			name: "without a fallback, adf machine parameters are dropped but content is kept",
			in: `<ac:adf-extension><ac:adf-node type="panel">` +
				`<ac:adf-attribute key="panel-type">info</ac:adf-attribute>` +
				`<ac:adf-content><p>body</p></ac:adf-content></ac:adf-node></ac:adf-extension>`,
			want: "body",
		},
		{
			name: "editor placeholders render nothing",
			in:   `<p>a<ac:placeholder>type here</ac:placeholder>b</p>`,
			want: "ab",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := md(tt.in); got != tt.want {
				t.Errorf("ToMarkdown(%q) =\n  %q\nwant\n  %q", tt.in, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ac:link, ac:image, ac:emoticon
// ---------------------------------------------------------------------------

func TestToMarkdownACLink(t *testing.T) {
	wiki := site + "/wiki"

	tests := []struct {
		name string
		in   string
		opts MarkdownOptions
		want string
	}{
		{
			name: "a page link renders a display URL",
			in: `<ac:link><ri:page ri:content-title="Other Page" ri:space-key="ENG"/>` +
				`<ac:plain-text-link-body><![CDATA[see this]]></ac:plain-text-link-body></ac:link>`,
			want: "[see this](" + wiki + "/display/ENG/Other+Page)",
		},
		{
			name: "a page link with no body falls back to the title and the page's space",
			in:   `<ac:link><ri:page ri:content-title="Other Page"/></ac:link>`,
			opts: MarkdownOptions{SpaceKey: "ENG"},
			want: "[Other Page](" + wiki + "/display/ENG/Other+Page)",
		},
		{
			name: "the link's own space key wins over the page's space",
			in:   `<ac:link><ri:page ri:content-title="P" ri:space-key="OTHER"/></ac:link>`,
			opts: MarkdownOptions{SpaceKey: "ENG"},
			want: "[P](" + wiki + "/display/OTHER/P)",
		},
		{
			name: "with no space at all the link degrades to plain text",
			in:   `<ac:link><ri:page ri:content-title="Other Page"/></ac:link>`,
			want: "Other Page",
		},
		{
			name: "a rich link body is rendered as inline Markdown",
			in: `<ac:link><ri:page ri:content-title="P" ri:space-key="S"/>` +
				`<ac:link-body><strong>bold</strong> label</ac:link-body></ac:link>`,
			want: "[**bold** label](" + wiki + "/display/S/P)",
		},
		{
			name: "an anchor on a page link is appended",
			in:   `<ac:link ac:anchor="Intro"><ri:page ri:content-title="P" ri:space-key="S"/></ac:link>`,
			want: "[P](" + wiki + "/display/S/P#Intro)",
		},
		{
			name: "a blog post link works like a page link",
			in:   `<ac:link><ri:blog-post ri:content-title="Post" ri:space-key="S"/></ac:link>`,
			want: "[Post](" + wiki + "/display/S/Post)",
		},
		{
			name: "a user mention resolves to a display name",
			in:   `<p>Owner: <ac:link><ri:user ri:account-id="abc123"/></ac:link></p>`,
			opts: MarkdownOptions{Users: map[string]string{"abc123": "Alex Example"}},
			want: "Owner: @Alex Example",
		},
		{
			name: "an unresolved mention degrades to a generic handle",
			in:   `<p>Owner: <ac:link><ri:user ri:account-id="abc123"/></ac:link></p>`,
			want: "Owner: @user",
		},
		{
			name: "an unresolved mention uses its link body when it has one",
			in: `<ac:link><ri:user ri:account-id="abc123"/>` +
				`<ac:plain-text-link-body><![CDATA[Ana]]></ac:plain-text-link-body></ac:link>`,
			want: "@Ana",
		},
		{
			name: "a legacy userkey mention also resolves",
			in:   `<ac:link><ri:user ri:userkey="legacy1"/></ac:link>`,
			opts: MarkdownOptions{Users: map[string]string{"legacy1": "Ana"}},
			want: "@Ana",
		},
		{
			name: "an attachment link points at the page's download URL",
			in:   `<ac:link><ri:attachment ri:filename="flow chart.png"/></ac:link>`,
			opts: MarkdownOptions{PageID: "42"},
			want: "[flow chart.png](" + wiki + "/download/attachments/42/flow%20chart.png)",
		},
		{
			name: "an attachment link with no page id degrades to the filename",
			in:   `<ac:link><ri:attachment ri:filename="a.pdf"/></ac:link>`,
			want: "a.pdf",
		},
		{
			name: "a space link points at the space home",
			in:   `<ac:link><ri:space ri:space-key="ENG"/></ac:link>`,
			want: "[ENG](" + wiki + "/spaces/ENG)",
		},
		{
			name: "a space link keeps its own label",
			in: `<ac:link><ri:space ri:space-key="ENG"/>` +
				`<ac:plain-text-link-body><![CDATA[the space]]></ac:plain-text-link-body></ac:link>`,
			want: "[the space](" + wiki + "/spaces/ENG)",
		},
		{
			name: "an external url link",
			in: `<ac:link><ri:url ri:value="https://x.dev"/>` +
				`<ac:plain-text-link-body><![CDATA[docs]]></ac:plain-text-link-body></ac:link>`,
			want: "[docs](https://x.dev)",
		},
		{
			name: "an external url link with no label uses the url as its label",
			in:   `<ac:link><ri:url ri:value="https://x.dev"/></ac:link>`,
			want: "[https://x.dev](https://x.dev)",
		},
		{
			name: "an anchor-only link points inside the current page",
			in: `<ac:link ac:anchor="Section One">` +
				`<ac:plain-text-link-body><![CDATA[jump]]></ac:plain-text-link-body></ac:link>`,
			want: "[jump](#Section%20One)",
		},
		{
			name: "an anchor-only link with no label uses the anchor text",
			in:   `<ac:link ac:anchor="Section One"/>`,
			want: "[Section One](#Section%20One)",
		},
		{
			name: "an empty ac:link renders nothing",
			in:   `<p>a<ac:link/>b</p>`,
			want: "ab",
		},
		{
			name: "brackets in a link label are escaped",
			in: `<ac:link><ri:page ri:content-title="P" ri:space-key="S"/>` +
				`<ac:plain-text-link-body><![CDATA[a [b] c]]></ac:plain-text-link-body></ac:link>`,
			want: `[a \[b\] c](` + wiki + "/display/S/P)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mdWith(tt.in, tt.opts); got != tt.want {
				t.Errorf("ToMarkdown(%q) =\n  %q\nwant\n  %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestToMarkdownACImage(t *testing.T) {
	wiki := site + "/wiki"

	tests := []struct {
		name string
		in   string
		opts MarkdownOptions
		want string
	}{
		{
			name: "an attachment image links to the page download URL",
			in:   `<ac:image ac:alt="Diagram"><ri:attachment ri:filename="flow chart.png"/></ac:image>`,
			opts: MarkdownOptions{PageID: "42"},
			want: "![Diagram](" + wiki + "/download/attachments/42/flow%20chart.png)",
		},
		{
			name: "the filename stands in when there is no alt text",
			in:   `<ac:image><ri:attachment ri:filename="a.png"/></ac:image>`,
			opts: MarkdownOptions{PageID: "42"},
			want: "![a.png](" + wiki + "/download/attachments/42/a.png)",
		},
		{
			name: "ac:title is used when ac:alt is missing",
			in:   `<ac:image ac:title="T"><ri:attachment ri:filename="a.png"/></ac:image>`,
			opts: MarkdownOptions{PageID: "42"},
			want: "![T](" + wiki + "/download/attachments/42/a.png)",
		},
		{
			name: "without a page id we still say an image was there",
			in:   `<ac:image ac:alt="Diagram"><ri:attachment ri:filename="a.png"/></ac:image>`,
			want: "(image: Diagram)",
		},
		{
			name: "an external image",
			in:   `<ac:image><ri:url ri:value="https://x.dev/a.png"/></ac:image>`,
			want: "![](https://x.dev/a.png)",
		},
		{
			name: "a site-relative external image is absolutised",
			in:   `<ac:image><ri:url ri:value="/download/a.png"/></ac:image>`,
			want: "![](" + site + "/download/a.png)",
		},
		{
			name: "an image with no resource at all renders nothing",
			in:   `<p>a<ac:image/>b</p>`,
			want: "ab",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mdWith(tt.in, tt.opts); got != tt.want {
				t.Errorf("ToMarkdown(%q) =\n  %q\nwant\n  %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestToMarkdownEmoticons(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"a known emoticon becomes its unicode character", `<p><ac:emoticon ac:name="tick"/> ok</p>`, "✅ ok"},
		{"thumbs up", `<p><ac:emoticon ac:name="thumbs-up"/></p>`, "👍"},
		{"the emoji fallback attribute wins over the name",
			`<p><ac:emoticon ac:name="tick" ac:emoji-fallback="🎉"/></p>`, "🎉"},
		{"an unknown name with a fallback uses the fallback",
			`<p><ac:emoticon ac:name="x" ac:emoji-fallback="🎉"/></p>`, "🎉"},
		{"an unknown name with a shortname uses the shortname",
			`<p><ac:emoticon ac:name="x" ac:emoji-shortname=":party:"/></p>`, ":party:"},
		{"an unknown name alone becomes a shortcode",
			`<p><ac:emoticon ac:name="mystery"/></p>`, ":mystery:"},
		{"names are matched case-insensitively", `<p><ac:emoticon ac:name="TICK"/></p>`, "✅"},
		{"an emoticon with nothing at all renders nothing", `<p>a<ac:emoticon/>b</p>`, "ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := md(tt.in); got != tt.want {
				t.Errorf("ToMarkdown(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestToMarkdownProfileMacro(t *testing.T) {
	in := `<p>Owner: <ac:structured-macro ac:name="profile">` +
		`<ac:parameter ac:name="user"><ri:user ri:account-id="u1"/></ac:parameter>` +
		`</ac:structured-macro></p>`

	t.Run("a resolved profile macro renders the display name inline", func(t *testing.T) {
		got := mdWith(in, MarkdownOptions{Users: map[string]string{"u1": "Ana"}})
		if want := "Owner: @Ana"; got != want {
			t.Errorf("ToMarkdown() = %q, want %q", got, want)
		}
	})
	t.Run("an unresolved profile macro degrades to a generic handle", func(t *testing.T) {
		if got := md(in); got != "Owner: @user" {
			t.Errorf("ToMarkdown() = %q, want %q", got, "Owner: @user")
		}
	})
}

func TestAccountIDsIn(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "every mention is listed exactly once, sorted",
			in: `<ac:link><ri:user ri:account-id="b"/></ac:link>` +
				`<ac:link><ri:user ri:account-id="a"/></ac:link>` +
				`<ac:link><ri:user ri:account-id="a"/></ac:link>`,
			want: []string{"a", "b"},
		},
		{
			name: "single quotes are accepted too",
			in:   `<ri:user ri:account-id='x'/>`,
			want: []string{"x"},
		},
		{
			name: "whitespace around the equals sign is tolerated",
			in:   `<ri:user ri:account-id = "x"/>`,
			want: []string{"x"},
		},
		{name: "no mentions yields nothing", in: `<p>text</p>`, want: nil},
		{name: "empty input yields nothing", in: ``, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AccountIDsIn(tt.in)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("AccountIDsIn(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// End to end
// ---------------------------------------------------------------------------

func TestToMarkdownRealisticPage(t *testing.T) {
	in := `<h2>Overview</h2><p>Owned by <ac:link><ri:user ri:account-id="u1"/></ac:link>.</p>` +
		`<table><tbody><tr><th>Status</th><td><ac:structured-macro ac:name="status">` +
		`<ac:parameter ac:name="title">DRAFT</ac:parameter></ac:structured-macro></td></tr></tbody></table>` +
		`<h2>Steps</h2><ol><li>First</li><li>Second</li></ol>`

	want := "## Overview\n\nOwned by @Ana.\n\n" +
		"|  |  |\n| --- | --- |\n| Status | `DRAFT` |\n\n" +
		"## Steps\n\n1. First\n2. Second"

	got := mdWith(in, MarkdownOptions{Users: map[string]string{"u1": "Ana"}})
	if got != want {
		t.Errorf("ToMarkdown() =\n%s\n\nwant\n%s", got, want)
	}
}

func TestToMarkdownOptions(t *testing.T) {
	t.Run("a trailing slash on the site is trimmed before links are built", func(t *testing.T) {
		got := ToMarkdown(`<p><a href="/x">x</a></p>`, MarkdownOptions{Site: site + "/"})
		if want := "[x](" + site + "/x)"; got != want {
			t.Errorf("ToMarkdown() = %q, want %q", got, want)
		}
	})
	t.Run("the wiki mount point is derived from the site when not given", func(t *testing.T) {
		got := ToMarkdown(`<ac:link><ri:space ri:space-key="S"/></ac:link>`, MarkdownOptions{Site: site})
		if want := "[S](" + site + "/wiki/spaces/S)"; got != want {
			t.Errorf("ToMarkdown() = %q, want %q", got, want)
		}
	})
	t.Run("an explicit wiki URL overrides the derived one", func(t *testing.T) {
		got := ToMarkdown(`<ac:link><ri:space ri:space-key="S"/></ac:link>`,
			MarkdownOptions{Site: site, Wiki: "https://wiki.example/w/"})
		if want := "[S](https://wiki.example/w/spaces/S)"; got != want {
			t.Errorf("ToMarkdown() = %q, want %q", got, want)
		}
	})
	t.Run("a nil Users map is not a panic", func(t *testing.T) {
		got := ToMarkdown(`<ac:link><ri:user ri:account-id="x"/></ac:link>`, MarkdownOptions{Site: site})
		if got != "@user" {
			t.Errorf("ToMarkdown() = %q, want %q", got, "@user")
		}
	})
	t.Run("zero options still produce output", func(t *testing.T) {
		if got := ToMarkdown("<p>hello</p>", MarkdownOptions{}); got != "hello" {
			t.Errorf("ToMarkdown() = %q, want %q", got, "hello")
		}
	})
}
