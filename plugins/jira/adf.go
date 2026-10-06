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
		attrs, _ := n["attrs"].(map[string]any)
		switch typ {
		case "mention":
			if v, ok := attrs["text"].(string); ok {
				inner.WriteString(v)
			} else if v, ok := attrs["id"].(string); ok {
				inner.WriteString("@" + v)
			}
		case "emoji":
			if v, ok := attrs["text"].(string); ok {
				inner.WriteString(v)
			} else if v, ok := attrs["shortName"].(string); ok {
				inner.WriteString(v)
			}
		case "inlineCard", "blockCard", "embedCard":
			if v, ok := attrs["url"].(string); ok {
				inner.WriteString(v)
			}
		case "rule":
			inner.WriteString("---\n")
		}
		if typ == "hardBreak" {
			inner.WriteString("\n")
		}
		if content, ok := n["content"]; ok {
			writeADF(&inner, content)
		}
		s := inner.String()
		if typ == "text" {
			if marks, ok := n["marks"].([]any); ok {
				for _, mark := range marks {
					m, ok := mark.(map[string]any)
					if !ok || m["type"] != "link" {
						continue
					}
					a, _ := m["attrs"].(map[string]any)
					href, _ := a["href"].(string)
					if href != "" && href != s {
						s += " (" + href + ")"
					}
				}
			}
		}
		if typ == "tableCell" || typ == "tableHeader" {
			s = strings.TrimSpace(s) + "\t"
		}
		if typ == "tableRow" {
			s = strings.TrimRight(s, "\t") + "\n"
		}

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
