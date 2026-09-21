package jira

import "strings"

// Jira REST v3 returns rich text (descriptions, comments) as ADF — Atlassian
// Document Format, a nested JSON node tree. For terminal display we want
// readable plain text, so we walk the tree and concatenate text nodes,
// inserting line breaks at block boundaries. Best-effort and total: an unknown
// node type simply contributes its children's text.
func adfToText(node any) string {
	var b strings.Builder
	writeADF(&b, node)
	return b.String()
}

var adfBlocks = map[string]bool{
	"paragraph": true, "heading": true, "blockquote": true,
	"listItem": true, "codeBlock": true,
}

func writeADF(b *strings.Builder, node any) {
	switch n := node.(type) {
	case nil:
		return
	case string:
		b.WriteString(n)
	case []any:
		for _, c := range n {
			writeADF(b, c)
		}
	case map[string]any:
		typ, _ := n["type"].(string)
		var inner strings.Builder
		if typ == "text" {
			if t, ok := n["text"].(string); ok {
				inner.WriteString(t)
			}
		}
		if typ == "hardBreak" {
			inner.WriteString("\n")
		}
		if content, ok := n["content"]; ok {
			writeADF(&inner, content)
		}
		s := inner.String()
		if typ == "listItem" {
			s = "- " + s
		}
		if adfBlocks[typ] {
			s += "\n"
		}
		b.WriteString(s)
	}
}

// text renders a Jira rich-text field, which may arrive as a plain string
// (older APIs) or as ADF.
func text(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(adfToText(v))
}
