package confluence

import (
	"strconv"
	"strings"
)

// A small, tolerant XHTML/XML parser.
//
// Confluence page bodies come back as "storage format": XHTML with namespaced
// Confluence elements (<ac:structured-macro>, <ri:page>, …) and CDATA sections
// holding raw macro bodies (code blocks in particular). A generic HTML parser
// would not understand the ac:/ri: vocabulary, so we parse it ourselves — no
// dependencies, fully testable.
//
// Tolerance rules: unknown tags are kept as-is, stray closing tags are ignored,
// unclosed tags are closed at the end of their parent, and attribute values may
// be double-, single-, or unquoted.

type nodeKind int

const (
	kindElement nodeKind = iota
	kindText
)

// Node is an element or a run of text.
type Node struct {
	Kind     nodeKind
	Name     string // element name, lowercased
	Attrs    map[string]string
	Children []*Node
	Text     string
	Raw      bool // text came from CDATA and was not entity-decoded
}

// IsElement reports whether n is an element, optionally of a given name.
func IsElement(n *Node, name ...string) bool {
	if n == nil || n.Kind != kindElement {
		return false
	}
	if len(name) == 0 {
		return true
	}
	return n.Name == name[0]
}

func (n *Node) Attr(key string) string {
	if n == nil || n.Attrs == nil {
		return ""
	}
	return n.Attrs[key]
}

// voidElements never push a scope, even without a self-closing slash.
var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
}

// entities are the named ones that actually show up in Confluence content.
// Anything else is left verbatim rather than mangled.
var entities = map[string]string{
	"amp": "&", "lt": "<", "gt": ">", "quot": `"`, "apos": "'", "nbsp": " ",
	"ndash": "–", "mdash": "—", "hellip": "…", "middot": "·", "bull": "•",
	"lsquo": "‘", "rsquo": "’", "ldquo": "“", "rdquo": "”",
	"laquo": "«", "raquo": "»", "deg": "°", "copy": "©", "reg": "®", "trade": "™",
	"times": "×", "divide": "÷", "plusmn": "±", "micro": "µ", "para": "¶", "sect": "§",
	"larr": "←", "rarr": "→", "harr": "↔", "uarr": "↑", "darr": "↓",
	"ne": "≠", "le": "≤", "ge": "≥", "infin": "∞", "check": "✓", "cross": "✗",
	"euro": "€", "pound": "£", "yen": "¥", "cent": "¢",
}

// DecodeEntities expands the entities above plus numeric references.
func DecodeEntities(s string) string {
	if !strings.ContainsRune(s, '&') {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '&' {
			b.WriteByte(s[i])
			i++
			continue
		}
		end := strings.IndexByte(s[i:], ';')
		if end == -1 || end > 32 {
			b.WriteByte(s[i])
			i++
			continue
		}
		name := s[i+1 : i+end]
		if repl, ok := expandEntity(name); ok {
			b.WriteString(repl)
			i += end + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func expandEntity(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	if name[0] == '#' {
		digits, base := name[1:], 10
		if len(digits) > 0 && (digits[0] == 'x' || digits[0] == 'X') {
			digits, base = digits[1:], 16
		}
		code, err := strconv.ParseInt(digits, base, 32)
		if err != nil || code < 0 || code > 0x10ffff || (code >= 0xd800 && code <= 0xdfff) {
			return "", false
		}
		return string(rune(code)), true
	}
	if v, ok := entities[name]; ok {
		return v, true
	}
	if v, ok := entities[strings.ToLower(name)]; ok {
		return v, true
	}
	return "", false
}

// ParseHTML builds a tolerant tree. The root is a synthetic "#root" element.
func ParseHTML(html string) *Node {
	root := &Node{Kind: kindElement, Name: "#root", Attrs: map[string]string{}}
	stack := []*Node{root}
	push := func(n *Node) { top := stack[len(stack)-1]; top.Children = append(top.Children, n) }
	pushText := func(t string, raw bool) {
		if t == "" {
			return
		}
		if raw {
			push(&Node{Kind: kindText, Text: t, Raw: true})
		} else {
			push(&Node{Kind: kindText, Text: DecodeEntities(t)})
		}
	}

	s := html
	for i := 0; i < len(s); {
		lt := strings.IndexByte(s[i:], '<')
		if lt == -1 {
			pushText(s[i:], false)
			break
		}
		lt += i
		if lt > i {
			pushText(s[i:lt], false)
		}

		switch {
		// <![CDATA[ … ]]> — macro bodies (code blocks). Content is verbatim.
		case strings.HasPrefix(s[lt:], "<![CDATA["):
			end := strings.Index(s[lt:], "]]>")
			if end == -1 {
				pushText(s[lt+9:], true)
				i = len(s)
			} else {
				pushText(s[lt+9:lt+end], true)
				i = lt + end + 3
			}
			continue

		// Comments and doctype/processing instructions are dropped.
		case strings.HasPrefix(s[lt:], "<!--"):
			end := strings.Index(s[lt:], "-->")
			if end == -1 {
				i = len(s)
			} else {
				i = lt + end + 3
			}
			continue

		case strings.HasPrefix(s[lt:], "<!"), strings.HasPrefix(s[lt:], "<?"):
			end := strings.IndexByte(s[lt:], '>')
			if end == -1 {
				i = len(s)
			} else {
				i = lt + end + 1
			}
			continue

		// Closing tag: unwind to the matching open element, ignore if unmatched.
		case lt+1 < len(s) && s[lt+1] == '/':
			end := strings.IndexByte(s[lt:], '>')
			var name string
			if end == -1 {
				name = s[lt+2:]
				i = len(s)
			} else {
				name = s[lt+2 : lt+end]
				i = lt + end + 1
			}
			name = strings.ToLower(strings.TrimSpace(name))
			for k := len(stack) - 1; k > 0; k-- {
				if stack[k].Name == name {
					stack = stack[:k]
					break
				}
			}
			continue
		}

		// Opening tag.
		name, after := scanTagName(s, lt)
		if name == "" {
			// A bare "<" in already-decoded text. Keep it.
			pushText("<", false)
			i = lt + 1
			continue
		}
		node := &Node{Kind: kindElement, Name: name, Attrs: map[string]string{}}
		j, selfClose := scanAttrs(s, after, node.Attrs)
		push(node)
		if !selfClose && !voidElements[name] {
			stack = append(stack, node)
		}
		i = j
	}
	return root
}

func scanTagName(s string, lt int) (string, int) {
	j := lt + 1
	if j >= len(s) {
		return "", j
	}
	c := s[j]
	if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_') {
		return "", j
	}
	k := j
	for k < len(s) {
		c := s[k]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
			c == '-' || c == '_' || c == '.' || c == ':' {
			k++
			continue
		}
		break
	}
	return strings.ToLower(s[j:k]), k
}

func scanAttrs(s string, j int, attrs map[string]string) (int, bool) {
	for j < len(s) {
		for j < len(s) && isSpaceByte(s[j]) {
			j++
		}
		if j >= len(s) {
			break
		}
		if s[j] == '>' {
			return j + 1, false
		}
		if s[j] == '/' && j+1 < len(s) && s[j+1] == '>' {
			return j + 2, true
		}
		start := j
		for j < len(s) {
			c := s[j]
			if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
				c == '-' || c == '_' || c == '.' || c == ':' {
				j++
				continue
			}
			break
		}
		if j == start {
			j++ // junk byte
			continue
		}
		name := strings.ToLower(s[start:j])
		for j < len(s) && isSpaceByte(s[j]) {
			j++
		}
		value := name // a valueless attribute is its own name (HTML boolean)
		if j < len(s) && s[j] == '=' {
			j++
			for j < len(s) && isSpaceByte(s[j]) {
				j++
			}
			if j < len(s) && (s[j] == '"' || s[j] == '\'') {
				q := s[j]
				end := strings.IndexByte(s[j+1:], q)
				if end == -1 {
					value = s[j+1:]
					j = len(s)
				} else {
					value = s[j+1 : j+1+end]
					j = j + end + 2
				}
			} else {
				start := j
				for j < len(s) && !isSpaceByte(s[j]) && s[j] != '>' {
					j++
				}
				value = s[start:j]
			}
		}
		attrs[name] = DecodeEntities(value)
	}
	return j, false
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// TextOf concatenates all descendant text, with no formatting.
func TextOf(n *Node) string {
	if n == nil {
		return ""
	}
	if n.Kind == kindText {
		return n.Text
	}
	var b strings.Builder
	for _, c := range n.Children {
		b.WriteString(TextOf(c))
	}
	return b.String()
}

// FindChild returns the first immediate child with the given tag name.
func FindChild(n *Node, name string) *Node {
	if n == nil {
		return nil
	}
	for _, c := range n.Children {
		if IsElement(c, name) {
			return c
		}
	}
	return nil
}

// ChildrenNamed returns all immediate children with the given tag name.
func ChildrenNamed(n *Node, name string) []*Node {
	if n == nil {
		return nil
	}
	var out []*Node
	for _, c := range n.Children {
		if IsElement(c, name) {
			out = append(out, c)
		}
	}
	return out
}
