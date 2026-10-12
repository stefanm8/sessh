package confluence

import (
	"cmp"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Confluence storage format (XHTML + ac:/ri: macros) -> Markdown.
//
// Beyond plain XHTML this understands the Confluence vocabulary that carries
// most of the meaning on a real wiki page: code macros, info/note/warning
// panels, expand blocks, status lozenges, task lists, page/user/attachment
// links, images and the new-editor layout wrappers.
//
// Everything is best-effort and total: an unrecognised element renders its
// children, and an unrecognised macro renders its body if it has one (or leaves
// an HTML comment naming it, so the reader knows content was there).

var emphasis = map[string]string{
	"strong": "**", "b": "**", "em": "*", "i": "*", "del": "~~", "s": "~~", "strike": "~~",
}

var headings = map[string]int{"h1": 1, "h2": 2, "h3": 3, "h4": 4, "h5": 5, "h6": 6}

// blockTags start their own block; everything else is inline.
var blockTags = map[string]bool{
	"p": true, "div": true, "section": true, "article": true, "main": true,
	"header": true, "footer": true, "aside": true, "nav": true,
	"figure": true, "figcaption": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"ul": true, "ol": true, "li": true, "table": true, "thead": true,
	"tbody": true, "tfoot": true, "tr": true, "td": true, "th": true,
	"pre": true, "blockquote": true, "hr": true, "dl": true, "dt": true,
	"dd": true, "details": true, "summary": true,
	"ac:layout": true, "ac:layout-section": true, "ac:layout-cell": true,
	"ac:task-list": true, "ac:task": true, "ac:rich-text-body": true,
	"ac:adf-extension": true, "ac:adf-node": true, "ac:adf-fallback": true,
	"ac:adf-content": true,
}

// inlineMacros live inside a sentence; treating them as blocks would split the
// paragraph they belong to.
var inlineMacros = map[string]bool{
	"status": true, "anchor": true, "profile": true, "profile-picture": true,
	"mention": true, "date": true, "metadata-from": true, "inline-comment": true,
}

// skipMacros render nothing useful outside Confluence (navigation, generated
// indexes, editor affordances). Dropping them beats a placeholder.
var skipMacros = map[string]bool{
	"toc": true, "toc-zone": true, "anchor": true, "children": true,
	"pagetree": true, "pagetreesearch": true, "livesearch": true,
	"recently-updated": true, "recently-updated-dashboard": true,
	"contributors": true, "contributors-summary": true,
	"create-from-template-button": true, "space-details": true,
	"spacejump": true, "navmap": true, "labels-list": true,
	"content-by-label": true, "blog-posts": true, "viewport-redirect": true,
	"sp-editor": true, "placeholder": true,
}

// langs maps Confluence code-macro language names to Markdown fence hints.
var langs = map[string]string{
	"none": "", "text": "", "plaintext": "", "html/xml": "html", "js": "javascript",
	"ts": "typescript", "py": "python", "sh": "bash", "shell": "bash", "yml": "yaml",
	"c#": "csharp", "c++": "cpp", "ps": "powershell", "ps1": "powershell",
	"actionscript3": "actionscript", "erl": "erlang", "vb": "vbnet",
}

// callouts maps panel macros to the label printed in the blockquote.
var callouts = map[string]string{
	"info": "Info", "note": "Note", "tip": "Tip", "warning": "Warning",
	"caution": "Caution", "error": "Error", "success": "Success",
	"panel": "", "note-panel": "Note",
}

var emoticons = map[string]string{
	"smile": "🙂", "sad": "🙁", "cheeky": "😜", "laugh": "😄", "wink": "😉",
	"thumbs-up": "👍", "thumbs-down": "👎", "information": "ℹ️", "tick": "✅",
	"cross": "❌", "warning": "⚠️", "question": "❓", "plus": "➕", "minus": "➖",
	"light-on": "💡", "light-off": "💡", "heart": "❤️", "broken-heart": "💔",
	"yellow-star": "⭐", "red-star": "⭐", "green-star": "⭐", "blue-star": "⭐",
}

// MarkdownOptions carries the context a body needs to render absolute links.
type MarkdownOptions struct {
	Site     string            // https://your-tenant.atlassian.net
	Wiki     string            // https://your-tenant.atlassian.net/wiki
	SpaceKey string            // space of the page, for bare page links
	PageID   string            // id of the page, for attachment URLs
	Users    map[string]string // accountId -> display name, for @mentions
}

type mdCtx struct {
	MarkdownOptions
	inTable bool
}

// ToMarkdown converts a storage-format (or plain HTML) string to Markdown.
func ToMarkdown(storage string, opts MarkdownOptions) string {
	opts.Site = strings.TrimRight(opts.Site, "/")
	if opts.Wiki == "" {
		opts.Wiki = opts.Site + "/wiki"
	} else {
		opts.Wiki = strings.TrimRight(opts.Wiki, "/")
	}
	ctx := &mdCtx{MarkdownOptions: opts}
	return tidy(renderBlocks(ParseHTML(storage).Children, ctx))
}

var accountIDRe = regexp.MustCompile(`ri:account-id\s*=\s*["']([^"']+)["']`)

// AccountIDsIn lists the account ids referenced by @mentions in a body, so the
// caller can resolve them to display names before rendering.
func AccountIDsIn(storage string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range accountIDRe.FindAllStringSubmatch(storage, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Classification
// ---------------------------------------------------------------------------

func macroName(n *Node) string { return strings.ToLower(n.Attr("ac:name")) }

func macroParams(n *Node) map[string]string {
	params := map[string]string{}
	for _, c := range n.Children {
		if IsElement(c, "ac:parameter") {
			if k := strings.ToLower(c.Attr("ac:name")); k != "" {
				params[k] = strings.TrimSpace(TextOf(c))
			}
		}
	}
	return params
}

func isBlockNode(n *Node) bool {
	if !IsElement(n) {
		return false
	}
	if n.Name == "ac:structured-macro" || n.Name == "ac:macro" {
		m := macroName(n)
		if inlineMacros[m] {
			return false
		}
		// The Jira macro is a single inline issue link when given a key, and a
		// results table when given a JQL query.
		if m == "jira" {
			return macroParams(n)["key"] == ""
		}
		return true
	}
	return blockTags[n.Name]
}

// ---------------------------------------------------------------------------
// Text helpers
// ---------------------------------------------------------------------------

var (
	crlfRe       = regexp.MustCompile(`\r\n?`)
	trailingWS   = regexp.MustCompile(`[ \t]+\n`)
	blankRunRe   = regexp.MustCompile(`\n{3,}`)
	escapeRe     = regexp.MustCompile(`([\\` + "`" + `*\[\]<])`)
	leadingMdRe  = regexp.MustCompile(`(?m)^([ \t]*)([#>])`)
	bracketRe    = regexp.MustCompile(`([\[\]])`)
	whitespaceRe = regexp.MustCompile(`\s+`)
	langClassRe  = regexp.MustCompile(`(?:^|\s)(?:language|brush|lang)[-:]([A-Za-z0-9+#-]+)`)
	langCleanRe  = regexp.MustCompile(`[^a-z0-9+#-]`)
)

// tidy collapses stray blank lines, keeps two-space hard breaks and trims.
func tidy(s string) string {
	s = crlfRe.ReplaceAllString(s, "\n")
	// A line ending in 2+ spaces is a Markdown hard break — keep exactly two.
	// A single trailing space is noise.
	s = trailingWS.ReplaceAllStringFunc(s, func(m string) string {
		if len(m) >= 3 {
			return "  \n"
		}
		return "\n"
	})
	s = blankRunRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// escapeText escapes what would otherwise read as Markdown syntax. Kept
// deliberately light: `_` is left alone so snake_case stays readable.
func escapeText(s string) string {
	s = escapeRe.ReplaceAllString(s, `\$1`)
	return leadingMdRe.ReplaceAllString(s, `$1\$2`)
}

// escapeLabel prepares text for use inside a [label].
func escapeLabel(s string) string {
	s = bracketRe.ReplaceAllString(s, `\$1`)
	return strings.TrimSpace(whitespaceRe.ReplaceAllString(s, " "))
}

// fence wraps code in a fence long enough to contain it.
func fence(code, lang string) string {
	body := strings.Trim(code, "\n")
	f := "```"
	for strings.Contains(body, f) {
		f += "`"
	}
	return f + lang + "\n" + body + "\n" + f
}

func quote(body string) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if l == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + l
		}
	}
	return strings.Join(lines, "\n")
}

// titlePath spells a title the way Confluence display URLs do.
func titlePath(title string) string {
	return strings.ReplaceAll(url.QueryEscape(title), "+", "%20")
}

func displayPath(title string) string {
	return strings.ReplaceAll(url.QueryEscape(title), "%20", "+")
}

func absolute(href string, ctx *mdCtx) string {
	h := strings.TrimSpace(href)
	if h == "" {
		return ""
	}
	lower := strings.ToLower(h)
	for _, p := range []string{"http:", "https:", "mailto:", "tel:", "#"} {
		if strings.HasPrefix(lower, p) {
			return h
		}
	}
	if strings.HasPrefix(h, "/") {
		return ctx.Site + h
	}
	return h
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

// renderBlocks renders sibling nodes as a sequence of Markdown blocks. Inline
// runs between block elements are gathered into their own paragraph.
func renderBlocks(nodes []*Node, ctx *mdCtx) string {
	return renderSequence(nodes, ctx, false)
}

// Within a list item, nested lists follow the preceding block without a blank
// line. Other blocks use the same inline buffering and rendering as page bodies.
func renderSequence(nodes []*Node, ctx *mdCtx, listItem bool) string {
	type part struct {
		text  string
		tight bool
	}
	var parts []part
	var buf strings.Builder
	flush := func() {
		if t := buf.String(); strings.TrimSpace(t) != "" {
			t = strings.Trim(t, " \t")
			if listItem {
				t = strings.TrimSpace(t)
			}
			parts = append(parts, part{text: t})
		}
		buf.Reset()
	}
	for _, n := range nodes {
		if !isBlockNode(n) {
			buf.WriteString(renderInline(n, ctx))
			continue
		}
		flush()
		tight := listItem && (n.Name == "ul" || n.Name == "ol")
		if b := renderBlock(n, ctx); tight || strings.TrimSpace(b) != "" {
			if !listItem {
				b = strings.Trim(b, "\n")
			}
			parts = append(parts, part{text: b, tight: tight})
		}
	}
	flush()
	for i, p := range parts {
		if i > 0 {
			buf.WriteByte('\n')
			if !p.tight {
				buf.WriteByte('\n')
			}
		}
		buf.WriteString(p.text)
	}
	return buf.String()
}

func inlineAll(nodes []*Node, ctx *mdCtx) string {
	var b strings.Builder
	for _, n := range nodes {
		b.WriteString(renderInline(n, ctx))
	}
	return b.String()
}

// renderChildren picks block or inline mode automatically.
func renderChildren(n *Node, ctx *mdCtx) string {
	for _, c := range n.Children {
		if isBlockNode(c) {
			return renderBlocks(n.Children, ctx)
		}
	}
	return inlineAll(n.Children, ctx)
}

func renderBlock(n *Node, ctx *mdCtx) string {
	if level, ok := headings[n.Name]; ok {
		text := strings.TrimSpace(whitespaceRe.ReplaceAllString(inlineAll(n.Children, ctx), " "))
		if text == "" {
			return ""
		}
		return strings.Repeat("#", level) + " " + text
	}

	switch n.Name {
	case "hr":
		return "---"
	case "br":
		return ""
	case "ul", "ol":
		return renderList(n, ctx)
	case "li":
		// Only reached for a stray <li> outside a list.
		return "- " + tidy(renderChildren(n, ctx))
	case "table":
		return renderTable(n, ctx)
	case "blockquote":
		return quote(tidy(renderChildren(n, ctx)))
	case "pre":
		code := FindChild(n, "code")
		lang := langFromClass(n.Attr("class"))
		if code != nil {
			lang = langFromClass(code.Attr("class"))
		}
		return fence(TextOf(cmp.Or(code, n)), lang)
	case "dl":
		return renderDefinitionList(n, ctx)
	case "ac:structured-macro", "ac:macro":
		return renderMacro(n, ctx)
	case "ac:task-list":
		return renderTaskList(n, ctx)
	case "ac:adf-extension":
		// New-editor extensions ship a human-readable fallback; prefer it so we
		// don't render the machine parameters as well.
		if fb := FindChild(n, "ac:adf-fallback"); fb != nil {
			return renderBlocks(fb.Children, ctx)
		}
		return renderBlocks(dropADFMeta(n.Children), ctx)
	case "ac:adf-node":
		return renderBlocks(dropADFMeta(n.Children), ctx)
	default:
		// Transparent containers: the new-editor layout, macro bodies, and
		// ordinary HTML grouping elements.
		return renderChildren(n, ctx)
	}
}

func dropADFMeta(kids []*Node) []*Node {
	out := make([]*Node, 0, len(kids))
	for _, c := range kids {
		if IsElement(c, "ac:adf-attribute") || IsElement(c, "ac:adf-parameter") {
			continue
		}
		out = append(out, c)
	}
	return out
}

func langFromClass(cls string) string {
	if m := langClassRe.FindStringSubmatch(cls); m != nil {
		return mapLang(m[1])
	}
	return ""
}

func mapLang(l string) string {
	k := strings.ToLower(strings.TrimSpace(l))
	if k == "" {
		return ""
	}
	if v, ok := langs[k]; ok {
		return v
	}
	return langCleanRe.ReplaceAllString(k, "")
}

func renderInline(n *Node, ctx *mdCtx) string {
	if n == nil {
		return ""
	}
	if n.Kind == kindText {
		if n.Raw {
			return n.Text
		}
		return escapeText(n.Text)
	}
	if isBlockNode(n) {
		return renderBlock(n, ctx)
	}

	switch n.Name {
	case "br":
		if ctx.inTable {
			return "<br>"
		}
		return "  \n"
	case "strong", "b", "em", "i", "del", "s", "strike":
		marker := emphasis[n.Name]
		if t := strings.TrimSpace(inlineAll(n.Children, ctx)); t != "" {
			return marker + t + marker
		}
		return ""
	case "code", "tt":
		t := TextOf(n)
		if strings.TrimSpace(t) == "" {
			return ""
		}
		ticks := "`"
		for strings.Contains(t, ticks) {
			ticks += "`"
		}
		pad := ""
		if strings.HasPrefix(t, "`") || strings.HasSuffix(t, "`") {
			pad = " "
		}
		return ticks + pad + t + pad + ticks
	case "a":
		// The text nodes underneath are already Markdown-escaped; only the
		// shape needs fixing, since a link label must be a single line.
		label := strings.TrimSpace(whitespaceRe.ReplaceAllString(inlineAll(n.Children, ctx), " "))
		href := absolute(n.Attr("href"), ctx)
		if href == "" {
			return label
		}
		if label == "" {
			return "<" + href + ">"
		}
		return "[" + label + "](" + href + ")"
	case "img":
		src := absolute(n.Attr("src"), ctx)
		alt := escapeLabel(cmp.Or(n.Attr("alt"), n.Attr("title")))
		if src == "" {
			return ""
		}
		return "![" + alt + "](" + src + ")"
	case "time":
		return escapeText(cmp.Or(n.Attr("datetime"), TextOf(n)))
	case "ac:link":
		return renderACLink(n, ctx)
	case "ac:image":
		return renderACImage(n, ctx)
	case "ac:emoticon":
		if fb := n.Attr("ac:emoji-fallback"); fb != "" {
			return fb
		}
		key := strings.ToLower(n.Attr("ac:name"))
		if e, ok := emoticons[key]; ok {
			return e
		}
		if short := n.Attr("ac:emoji-shortname"); short != "" {
			return short
		}
		if key != "" {
			return ":" + key + ":"
		}
		return ""
	case "ac:structured-macro", "ac:macro":
		return renderMacro(n, ctx)
	// Editor bookkeeping that carries no content.
	case "ac:placeholder", "ac:adf-attribute", "ac:adf-parameter", "ac:parameter":
		return ""
	// Resource identifiers are consumed by their parent (ac:link / ac:image).
	case "ri:page", "ri:user", "ri:attachment", "ri:space", "ri:blog-post",
		"ri:url", "ri:content-entity":
		return ""
	default:
		return renderChildren(n, ctx)
	}
}

// --- lists ------------------------------------------------------------------

func renderList(n *Node, ctx *mdCtx) string {
	ordered := n.Name == "ol"
	start := 1
	if v, err := strconv.Atoi(n.Attr("start")); err == nil && v != 0 {
		start = v
	}
	items := ChildrenNamed(n, "li")
	if len(items) == 0 {
		// A list with no <li> children still deserves its content rendered.
		return renderChildren(n, ctx)
	}

	var lines []string
	for idx, li := range items {
		marker := "- "
		if ordered {
			marker = strconv.Itoa(start+idx) + ". "
		}
		lines = append(lines, indentItem(tidy(renderSequence(li.Children, ctx, true)), marker))
	}
	return strings.Join(lines, "\n")
}

// indentItem aligns continuation lines under list and task-list text.
func indentItem(body, marker string) string {
	parts := strings.Split(body, "\n")
	parts[0] = marker + parts[0]
	indent := strings.Repeat(" ", len(marker))
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = indent + parts[i]
		}
	}
	return strings.Join(parts, "\n")
}

func renderDefinitionList(n *Node, ctx *mdCtx) string {
	var out []string
	for _, c := range n.Children {
		switch {
		case IsElement(c, "dt"):
			out = append(out, "**"+strings.TrimSpace(inlineAll(c.Children, ctx))+"**")
		case IsElement(c, "dd"):
			out = append(out, ": "+tidy(renderChildren(c, ctx)))
		}
	}
	return strings.Join(out, "\n")
}

// --- tables -----------------------------------------------------------------

func collectRows(n *Node, out []*Node) []*Node {
	for _, c := range n.Children {
		if IsElement(c, "tr") {
			out = append(out, c)
		} else if IsElement(c) {
			out = collectRows(c, out)
		}
	}
	return out
}

func renderTable(n *Node, ctx *mdCtx) string {
	cellCtx := *ctx
	cellCtx.inTable = true
	var grid [][]string
	width, headerRow := 0, false
	for _, tr := range collectRows(n, nil) {
		var row []string
		allHeaders := true
		for _, cell := range tr.Children {
			if !IsElement(cell, "td") && !IsElement(cell, "th") {
				continue
			}
			span := 1
			if v, err := strconv.Atoi(cell.Attr("colspan")); err == nil && v > 1 {
				span = v
			}
			allHeaders = allHeaders && cell.Name == "th"
			row = append(row, cellText(cell, &cellCtx))
			// Expand colspans into empty cells so every row lines up.
			row = append(row, make([]string, span-1)...)
		}
		if len(row) > 0 {
			if len(grid) == 0 {
				headerRow = allHeaders
			}
			width = max(width, len(row))
			grid = append(grid, row)
		}
	}
	if len(grid) == 0 {
		return ""
	}
	line := func(row []string) string {
		cells := make([]string, width)
		copy(cells, row)
		return "| " + strings.Join(cells, " | ") + " |"
	}

	// A first column of <th> cells is not a header row. Only a complete first
	// row of <th> cells supplies headers; otherwise add an empty header row.
	var header []string
	if headerRow && len(grid[0]) == width {
		header, grid = grid[0], grid[1:]
	}
	lines := []string{line(header), "| " + strings.Repeat("--- | ", width-1) + "--- |"}
	for _, row := range grid {
		lines = append(lines, line(row))
	}
	return strings.Join(lines, "\n")
}

// cellText squeezes a cell onto one line: blocks become <br>-separated. (A
// fenced code block cannot survive that — Markdown tables cannot hold one.)
func cellText(cell *Node, ctx *mdCtx) string {
	t := tidy(renderChildren(cell, ctx))
	t = strings.ReplaceAll(t, "|", `\|`)
	t = strings.ReplaceAll(t, "\n\n", "<br><br>")
	return strings.ReplaceAll(t, "\n", "<br>")
}

// --- Confluence links, images, tasks ----------------------------------------

func renderACLink(n *Node, ctx *mdCtx) string {
	anchor := ""
	if a := n.Attr("ac:anchor"); a != "" {
		anchor = "#" + titlePath(a)
	}
	var label string
	if plain := FindChild(n, "ac:plain-text-link-body"); plain != nil {
		label = escapeLabel(TextOf(plain))
	} else if rich := FindChild(n, "ac:link-body"); rich != nil {
		label = strings.TrimSpace(whitespaceRe.ReplaceAllString(inlineAll(rich.Children, ctx), " "))
	}

	link := func(href, text string) string {
		if href == "" {
			return text
		}
		if text == "" {
			text = href
		}
		return "[" + text + "](" + href + ")"
	}

	if user := FindChild(n, "ri:user"); user != nil {
		id := cmp.Or(user.Attr("ri:account-id"), user.Attr("ri:userkey"))
		if name, ok := ctx.Users[id]; ok && name != "" {
			return "@" + name
		}
		if label != "" {
			return "@" + label
		}
		return "@user"
	}

	page := FindChild(n, "ri:page")
	if page == nil {
		page = FindChild(n, "ri:blog-post")
	}
	if page != nil {
		title := page.Attr("ri:content-title")
		space := cmp.Or(page.Attr("ri:space-key"), ctx.SpaceKey)
		href := ""
		if space != "" {
			href = ctx.Wiki + "/display/" + url.PathEscape(space) + "/" + displayPath(title) + anchor
		}
		return link(href, cmp.Or(label, escapeLabel(title)))
	}

	if att := FindChild(n, "ri:attachment"); att != nil {
		file := att.Attr("ri:filename")
		href := ""
		if ctx.PageID != "" {
			href = ctx.Wiki + "/download/attachments/" + ctx.PageID + "/" + url.PathEscape(file)
		}
		return link(href, cmp.Or(label, escapeLabel(file)))
	}

	if space := FindChild(n, "ri:space"); space != nil {
		key := space.Attr("ri:space-key")
		return link(ctx.Wiki+"/spaces/"+url.PathEscape(key), cmp.Or(label, escapeLabel(key)))
	}

	if u := FindChild(n, "ri:url"); u != nil {
		return link(u.Attr("ri:value"), label)
	}

	// A bare <ac:link ac:anchor="…"> points inside the current page.
	if anchor != "" {
		return link(anchor, cmp.Or(label, escapeLabel(n.Attr("ac:anchor"))))
	}
	return label
}

func renderACImage(n *Node, ctx *mdCtx) string {
	name := cmp.Or(n.Attr("ac:alt"), n.Attr("ac:title"))
	att := FindChild(n, "ri:attachment")
	u := FindChild(n, "ri:url")

	src := ""
	if att != nil {
		file := att.Attr("ri:filename")
		if name == "" {
			name = file
		}
		if ctx.PageID != "" {
			src = ctx.Wiki + "/download/attachments/" + ctx.PageID + "/" + url.PathEscape(file)
		}
	} else if u != nil {
		src = absolute(u.Attr("ri:value"), ctx)
	}
	if src == "" {
		if name != "" {
			return "(image: " + name + ")"
		}
		return ""
	}
	return "![" + escapeLabel(name) + "](" + src + ")"
}

func renderTaskList(n *Node, ctx *mdCtx) string {
	var lines []string
	for _, t := range ChildrenNamed(n, "ac:task") {
		status := strings.ToLower(strings.TrimSpace(TextOf(FindChild(t, "ac:task-status"))))
		body := ""
		if bodyEl := FindChild(t, "ac:task-body"); bodyEl != nil {
			body = tidy(renderChildren(bodyEl, ctx))
		}
		box := "[ ]"
		if status == "complete" {
			box = "[x]"
		}
		lines = append(lines, indentItem(body, "- "+box+" "))
	}
	return strings.Join(lines, "\n")
}

// --- macros -----------------------------------------------------------------

func renderMacro(n *Node, ctx *mdCtx) string {
	name := macroName(n)
	if skipMacros[name] {
		return ""
	}
	params := macroParams(n)
	plain := TextOf(FindChild(n, "ac:plain-text-body"))
	richEl := FindChild(n, "ac:rich-text-body")
	rich := func() string {
		if richEl == nil {
			return ""
		}
		return tidy(renderBlocks(richEl.Children, ctx))
	}

	switch name {
	case "code":
		body := fence(plain, mapLang(cmp.Or(params["language"], params["lang"])))
		if t := params["title"]; t != "" {
			return "**" + t + "**\n\n" + body
		}
		return body

	case "noformat", "preformatted":
		return fence(plain, "")

	case "info", "note", "tip", "warning", "caution", "error", "success", "panel", "note-panel":
		label := callouts[name]
		title := params["title"]
		head := ""
		switch {
		case label != "" && title != "":
			head = "**" + label + ": " + title + "**"
		case label != "":
			head = "**" + label + "**"
		case title != "":
			head = "**" + title + "**"
		}
		body := cmp.Or(rich(), plain)
		inner := body
		if head != "" {
			if body != "" {
				inner = head + "\n\n" + body
			} else {
				inner = head
			}
		}
		if inner == "" {
			return ""
		}
		return quote(inner)

	case "expand":
		title := cmp.Or(params["title"], "Details")
		body := cmp.Or(rich(), plain)
		return "<details>\n<summary>" + title + "</summary>\n\n" + body + "\n\n</details>"

	case "status":
		if t := params["title"]; t != "" {
			return "`" + t + "`"
		}
		return ""

	case "jira":
		if key := params["key"]; key != "" {
			return "[" + key + "](" + ctx.Site + "/browse/" + url.PathEscape(key) + ")"
		}
		if q := cmp.Or(params["jqlquery"], params["jql"]); q != "" {
			return "<!-- Jira issues macro: " + q + " -->"
		}
		return "<!-- Jira issues macro -->"

	case "profile":
		// <ac:parameter ac:name="user"><ri:user ri:account-id="…"/></ac:parameter>
		for _, c := range n.Children {
			if IsElement(c, "ac:parameter") && strings.ToLower(c.Attr("ac:name")) == "user" {
				if u := FindChild(c, "ri:user"); u != nil {
					id := cmp.Or(u.Attr("ri:account-id"), u.Attr("ri:userkey"))
					if name, ok := ctx.Users[id]; ok && name != "" {
						return "@" + name
					}
				}
			}
		}
		return "@user"

	// Layout / grouping macros: the body is the content.
	case "section", "column", "excerpt", "div", "span", "details":
		return rich()

	case "include", "excerpt-include":
		return "<!-- included content: " + cmp.Or(params["0"], params["page"], "another page") + " -->"

	default:
		if body := rich(); body != "" {
			return body
		}
		if strings.TrimSpace(plain) != "" {
			return fence(plain, "")
		}
		title := ""
		if t := params["title"]; t != "" {
			title = fmt.Sprintf(" (%s)", t)
		}
		return "<!-- confluence macro: " + name + title + " -->"
	}
}
