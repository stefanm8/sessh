package teams

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Agents and humans want different things from the same rows, so all five
// formats live here rather than in the commands. json/jsonl are the machine
// contract — compact by default, stable key order, and a DERIVED ISO `time`
// field so no caller ever parses Teams' epoch-milliseconds-as-a-string. table
// and text are for humans (local times, truncated bodies); csv is for
// spreadsheets. Keeping the output deterministic is what makes it diffable.
//
// Output field names and layout are kept stable for scripts that parse them.

const (
	formatJSON  = "json"
	formatJSONL = "jsonl"
	formatTable = "table"
	formatText  = "text"
	formatCSV   = "csv"
)

var outputFormats = []string{formatJSON, formatJSONL, formatTable, formatText, formatCSV}

// resolveFormat: an explicit -f wins, otherwise json when piped and table on a
// terminal — the same "human by default, machine when redirected" rule the rest
// of the hub follows.
func resolveFormat(explicit string, isTTY bool) (string, error) {
	if explicit == "" {
		if isTTY {
			return formatTable, nil
		}
		return formatJSON, nil
	}
	f := strings.ToLower(explicit)
	if !slices.Contains(outputFormats, f) {
		return "", fmt.Errorf("invalid --format %q. Expected one of: %s", explicit, strings.Join(outputFormats, ", "))
	}
	return f, nil
}

func parseFields(list string) []string {
	if list == "" {
		return nil
	}
	var out []string
	for _, s := range strings.Split(list, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Ordered JSON records
// ---------------------------------------------------------------------------

// record is a JSON object with a fixed key order. Key order is part of the
// output contract here, and a Go map cannot promise one.
type record []field

type field struct {
	key string
	val any
}

func (r record) get(key string) any {
	for _, f := range r {
		if f.key == key {
			return f.val
		}
	}
	return nil
}

func (r record) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range r {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := jsonBytes(f.key, false)
		if err != nil {
			return nil, err
		}
		v, err := jsonBytes(f.val, false)
		if err != nil {
			return nil, fmt.Errorf("encoding field %q: %w", f.key, err)
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// jsonBytes encodes without HTML escaping to keep the `<`, `>` and `&`
// characters in message bodies readable.
func jsonBytes(v any, pretty bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if pretty {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// nullable renders an absent string as JSON null so a projected column keeps
// its key on every row — a missing sender must still yield a `sender` key,
// or downstream code has to special-case ragged objects.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ---------------------------------------------------------------------------
// Message records
// ---------------------------------------------------------------------------

// decorateMessage is the canonical output shape of a message.
func decorateMessage(m *Message, raw bool) record {
	rec := projectMessage(m, messageDefaultFields)
	if raw && len(m.Raw) > 0 {
		rec = append(rec, field{"raw", m.Raw})
	}
	return rec
}

// messageField holds the --fields getters. The aliases (conversation, type,
// timeLocal) exist because they make far better table and csv column headings
// than the wire names do.
var messageField = map[string]func(*Message) any{
	"id":                func(m *Message) any { return nullable(m.ID) },
	"time":              func(m *Message) any { return nullable(ToISO(MessageTimeMS(m))) },
	"timeLocal":         func(m *Message) any { return nullable(ToLocal(MessageTimeMS(m))) },
	"conversation":      func(m *Message) any { return nullable(m.ConversationID) },
	"conversationId":    func(m *Message) any { return nullable(m.ConversationID) },
	"conversationTitle": func(m *Message) any { return nullable(m.ConversationTitle) },
	"conversationType":  func(m *Message) any { return nullable(m.ConversationType) },
	"channelName":       func(m *Message) any { return nullable(m.ChannelName) },
	"teamId":            func(m *Message) any { return nullable(m.TeamID) },
	"teamName":          func(m *Message) any { return nullable(m.TeamName) },
	"sender":            func(m *Message) any { return nullable(m.Sender) },
	"authorMRI":         func(m *Message) any { return nullable(m.AuthorMRI) },
	"authorStatus":      func(m *Message) any { return nullable(m.AuthorStatus) },
	"body":              func(m *Message) any { return nullable(m.Body) },
	"contentType":       func(m *Message) any { return nullable(m.ContentType) },
	"type":              func(m *Message) any { return nullable(m.MessageType.String()) },
	"messageType":       func(m *Message) any { return nullable(m.MessageType.String()) },
	"source":            func(m *Message) any { return nullable(m.Source) },
	"composeTime":       func(m *Message) any { return nullable(m.ComposeTime.String()) },
	"capturedAt":        func(m *Message) any { return nullable(m.CapturedAt) },
	"raw": func(m *Message) any {
		if len(m.Raw) == 0 {
			return nil
		}
		return m.Raw
	},
}

func projectMessage(m *Message, fields []string) record {
	rec := make(record, 0, len(fields))
	for _, f := range fields {
		if get, ok := messageField[f]; ok {
			rec = append(rec, field{f, get(m)})
		} else {
			rec = append(rec, field{f, nil})
		}
	}
	return rec
}

var (
	messageDefaultFields = []string{
		"id", "time", "conversationId", "conversationTitle", "conversationType",
		"channelName", "teamId", "teamName", "sender", "authorMRI", "authorStatus",
		"body", "contentType", "messageType", "source", "composeTime", "capturedAt",
	}
	tableDefaultFields = []string{"timeLocal", "conversationTitle", "sender", "body"}
	csvDefaultFields   = []string{"time", "conversationTitle", "sender", "conversationId", "messageType", "body"}
	messageTruncate    = map[string]int{"conversationTitle": 36, "body": 70}
)

type renderOpts struct {
	format string
	fields []string
	raw    bool
	pretty bool
}

func renderMessages(rows []*Message, o renderOpts) (string, error) {
	project := func(cols []string) []record {
		recs := make([]record, len(rows))
		for i, m := range rows {
			recs[i] = projectMessage(m, cols)
		}
		return recs
	}

	switch o.format {
	case formatJSON, formatJSONL:
		recs := make([]record, len(rows))
		for i, m := range rows {
			if o.fields != nil {
				recs[i] = projectMessage(m, o.fields)
			} else {
				recs[i] = decorateMessage(m, o.raw)
			}
		}
		if o.format == formatJSON {
			return toJSON(recs, o.pretty)
		}
		return toJSONL(recs)

	case formatCSV:
		cols := o.fields
		if cols == nil {
			cols = csvDefaultFields
		}
		return toCSV(project(cols), cols), nil

	case formatText:
		blocks := make([]string, len(rows))
		for i, m := range rows {
			where := cmp.Or(m.ConversationTitle, m.ConversationID, "(unknown conversation)")
			who := authorLabel(m)
			kind := ""
			if t := m.MessageType.String(); t != "" {
				kind = " [" + t + "]"
			}
			blocks[i] = fmt.Sprintf("%s  %s — %s%s\n  %s",
				ToLocal(MessageTimeMS(m)), where, who, kind, strings.TrimSpace(m.Body))
		}
		return strings.Join(blocks, "\n\n"), nil
	}

	cols := o.fields
	if cols == nil {
		cols = tableDefaultFields
	}
	return toTable(project(cols), cols, messageTruncate), nil
}

// renderRows renders the aggregate commands' rows (conversations, senders),
// which are already ordered records rather than messages.
func renderRows(rows []record, o renderOpts, columns []string, truncate map[string]int) (string, error) {
	if o.fields != nil {
		columns = o.fields
		projected := make([]record, len(rows))
		for i, r := range rows {
			rec := make(record, 0, len(o.fields))
			for _, f := range o.fields {
				rec = append(rec, field{f, r.get(f)})
			}
			projected[i] = rec
		}
		rows = projected
	} else if columns == nil && len(rows) > 0 {
		for _, f := range rows[0] {
			columns = append(columns, f.key)
		}
	}

	switch o.format {
	case formatJSONL:
		return toJSONL(rows)
	case formatCSV:
		return toCSV(rows, columns), nil
	case formatTable, formatText:
		return toTable(rows, columns, truncate), nil
	default:
		return toJSON(rows, o.pretty)
	}
}

// ---------------------------------------------------------------------------
// Writers
// ---------------------------------------------------------------------------

func toJSON(rows []record, pretty bool) (string, error) {
	if rows == nil {
		rows = []record{} // an empty result is `[]`, never `null`
	}
	b, err := jsonBytes(rows, pretty)
	if err != nil {
		return "", fmt.Errorf("rendering json: %w", err)
	}
	return string(b), nil
}

func toJSONL(rows []record) (string, error) {
	lines := make([]string, len(rows))
	for i, r := range rows {
		b, err := jsonBytes(r, false)
		if err != nil {
			return "", fmt.Errorf("rendering jsonl row %d: %w", i, err)
		}
		lines[i] = string(b)
	}
	return strings.Join(lines, "\n"), nil
}

func cellText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case bool:
		return strconv.FormatBool(t)
	default:
		b, err := jsonBytes(v, false)
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(b)
	}
}

var csvNeedsQuote = regexp.MustCompile(`[",\n\r]`)

func csvCell(v any) string {
	s := cellText(v)
	if csvNeedsQuote.MatchString(s) {
		s = `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

func toCSV(rows []record, columns []string) string {
	header := make([]string, len(columns))
	for i, c := range columns {
		header[i] = csvCell(c)
	}
	lines := []string{strings.Join(header, ",")}
	for _, r := range rows {
		cells := make([]string, len(columns))
		for i, c := range columns {
			cells[i] = csvCell(r.get(c))
		}
		lines = append(lines, strings.Join(cells, ","))
	}
	return strings.Join(lines, "\n")
}

var whitespaceRun = regexp.MustCompile(`\s+`)

func toTable(rows []record, columns []string, truncate map[string]int) string {
	if len(columns) == 0 {
		return "(no rows)"
	}
	disp := make([][]string, len(rows))
	for i, r := range rows {
		cells := make([]string, len(columns))
		for j, c := range columns {
			cells[j] = truncateCell(whitespaceRun.ReplaceAllString(cellText(r.get(c)), " "), truncate[c])
		}
		disp[i] = cells
	}

	widths := make([]int, len(columns))
	for i, c := range columns {
		widths[i] = utf8.RuneCountInString(c)
	}
	for _, row := range disp {
		for i, cell := range row {
			if n := utf8.RuneCountInString(cell); n > widths[i] {
				widths[i] = n
			}
		}
	}

	pad := func(s string, w int) string {
		if n := w - utf8.RuneCountInString(s); n > 0 {
			return s + strings.Repeat(" ", n)
		}
		return s
	}
	line := func(cells []string) string {
		padded := make([]string, len(cells))
		for i, s := range cells {
			padded[i] = pad(s, widths[i])
		}
		return strings.Join(padded, "  ")
	}

	rule := make([]string, len(widths))
	for i, w := range widths {
		rule[i] = strings.Repeat("-", w)
	}

	out := []string{line(columns), strings.Join(rule, "  ")}
	for _, row := range disp {
		out = append(out, line(row))
	}
	return strings.Join(out, "\n")
}

// truncateCell cuts to max runes (not bytes, so accented names and emoji do not
// lose half a character) and marks the cut with an ellipsis.
func truncateCell(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max-1]) + "…"
}
