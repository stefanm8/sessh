package teams

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// sampleMessage is the fixture the Node CLI's format tests used, so the ported
// output contracts can be compared byte for byte.
func sampleMessage() *Message {
	return &Message{
		ID: "m2", ConversationID: "convA", Sender: "Alice",
		AuthorMRI: "8:orgid:alice", AuthorStatus: authorResolved,
		Body:              "hello, \"deploy\"\nplease",
		ConversationTitle: "Backend Team", ConversationType: "channel",
		ChannelName: "Backend", TeamID: "team-1", TeamName: "Engineering",
		ContentType: "Text", MessageType: "RichText/Html",
		ComposeTime: "1700000100000", CapturedAt: "2023-11-14T22:15:00.000Z",
		Source: "idb", Raw: json.RawMessage(`{"secret":"x"}`),
	}
}

func TestResolveFormat(t *testing.T) {
	tests := []struct {
		name     string
		explicit string
		isTTY    bool
		want     string
		wantErr  bool
	}{
		{name: "a terminal gets the human table", isTTY: true, want: formatTable},
		{name: "a pipe gets machine-readable json", isTTY: false, want: formatJSON},
		{name: "an explicit format wins on a terminal", explicit: "json", isTTY: true, want: formatJSON},
		{name: "an explicit format wins when piped", explicit: "table", isTTY: false, want: formatTable},
		{name: "jsonl", explicit: "jsonl", want: formatJSONL},
		{name: "csv", explicit: "csv", want: formatCSV},
		{name: "text", explicit: "text", want: formatText},
		{name: "the format name is case-insensitive", explicit: "JSON", want: formatJSON},
		{name: "an unknown format is a usage error", explicit: "yaml", wantErr: true},
		{name: "a near-miss is still an error", explicit: "tables", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveFormat(tt.explicit, tt.isTTY)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveFormat(%q) = %q, want an error", tt.explicit, got)
				}
				for _, f := range outputFormats {
					if !strings.Contains(err.Error(), f) {
						t.Errorf("error %q does not list the valid format %q", err, f)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveFormat(%q) error = %v", tt.explicit, err)
			}
			if got != tt.want {
				t.Errorf("resolveFormat(%q, tty=%v) = %q, want %q", tt.explicit, tt.isTTY, got, tt.want)
			}
		})
	}
}

func TestParseFields(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"an empty list projects nothing", "", nil},
		{"a single field", "id", []string{"id"}},
		{"several fields", "time,sender,body", []string{"time", "sender", "body"}},
		{"whitespace around names is trimmed", " time , sender ", []string{"time", "sender"}},
		{"empty entries are dropped", "time,,sender,", []string{"time", "sender"}},
		{"a list of only separators projects nothing", ",,,", nil},
		{"whitespace only projects nothing", "   ", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseFields(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseFields(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestDecorateMessage(t *testing.T) {
	m := sampleMessage()

	t.Run("the key order is part of the contract", func(t *testing.T) {
		rec := decorateMessage(m, false)
		want := []string{
			"id", "time", "conversationId", "conversationTitle", "conversationType",
			"channelName", "teamId", "teamName", "sender", "authorMRI", "authorStatus",
			"body", "contentType", "messageType",
			"source", "composeTime", "capturedAt",
		}
		var got []string
		for _, f := range rec {
			got = append(got, f.key)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("keys = %v, want %v", got, want)
		}
	})

	t.Run("time is derived so no caller parses epoch-ms themselves", func(t *testing.T) {
		if got := decorateMessage(m, false).get("time"); got != any("2023-11-14T22:15:00.000Z") {
			t.Errorf("time = %v, want the derived ISO stamp", got)
		}
	})

	t.Run("raw is dropped by default", func(t *testing.T) {
		if got := decorateMessage(m, false).get("raw"); got != nil {
			t.Errorf("raw = %v, want it omitted", got)
		}
	})

	t.Run("raw is included only when opted in", func(t *testing.T) {
		rec := decorateMessage(m, true)
		if rec.get("raw") == nil {
			t.Fatal("raw should be present with --raw")
		}
		if rec[len(rec)-1].key != "raw" {
			t.Errorf("raw should come last, got %q", rec[len(rec)-1].key)
		}
	})

	t.Run("--raw on a message with no payload adds no key", func(t *testing.T) {
		bare := &Message{ID: "x"}
		if got := decorateMessage(bare, true).get("raw"); got != nil {
			t.Errorf("raw = %v, want no key when there is no payload", got)
		}
	})

	t.Run("absent strings become JSON null so columns stay square", func(t *testing.T) {
		rec := decorateMessage(&Message{ID: "x"}, false)
		if rec.get("sender") != nil {
			t.Errorf("sender = %v, want nil", rec.get("sender"))
		}
		b, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"sender":null`) {
			t.Errorf("json = %s, want a null sender key", b)
		}
	})

	t.Run("an unknown time renders as null rather than a fake date", func(t *testing.T) {
		rec := decorateMessage(&Message{ID: "x"}, false)
		if rec.get("time") != nil {
			t.Errorf("time = %v, want nil", rec.get("time"))
		}
	})
}

func TestProjectMessage(t *testing.T) {
	m := sampleMessage()

	t.Run("fields are projected in the order asked for", func(t *testing.T) {
		rec := projectMessage(m, []string{"body", "id"})
		if len(rec) != 2 || rec[0].key != "body" || rec[1].key != "id" {
			t.Errorf("record = %+v, want body then id", rec)
		}
	})

	t.Run("aliases resolve to the same value as the wire names", func(t *testing.T) {
		rec := projectMessage(m, []string{"conversation", "conversationId", "type", "messageType"})
		if rec.get("conversation") != rec.get("conversationId") {
			t.Error("conversation and conversationId should agree")
		}
		if rec.get("type") != rec.get("messageType") {
			t.Error("type and messageType should agree")
		}
	})

	t.Run("timeLocal is the human rendering of the same instant", func(t *testing.T) {
		got := projectMessage(m, []string{"timeLocal"}).get("timeLocal")
		want := any(ToLocal(MessageTimeMS(m)))
		if got != want {
			t.Errorf("timeLocal = %v, want %v", got, want)
		}
	})

	t.Run("an unknown field keeps its key with a null value", func(t *testing.T) {
		rec := projectMessage(m, []string{"nope"})
		if len(rec) != 1 || rec[0].key != "nope" || rec[0].val != nil {
			t.Errorf("record = %+v, want a null 'nope' key", rec)
		}
	})
}

func TestRenderMessagesJSON(t *testing.T) {
	m := sampleMessage()

	t.Run("json is compact by default", func(t *testing.T) {
		out, err := renderMessages([]*Message{m}, renderOpts{format: formatJSON})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "\n") {
			t.Errorf("compact json should be one line, got:\n%s", out)
		}
		if !strings.HasPrefix(out, `[{"id":"m2"`) {
			t.Errorf("json = %s", out)
		}
	})

	t.Run("--pretty indents", func(t *testing.T) {
		out, err := renderMessages([]*Message{m}, renderOpts{format: formatJSON, pretty: true})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "\n  ") {
			t.Errorf("pretty json should be indented, got:\n%s", out)
		}
	})

	t.Run("a projection is byte-stable", func(t *testing.T) {
		out, err := renderMessages([]*Message{m}, renderOpts{
			format: formatJSON, fields: []string{"id", "sender", "body"},
		})
		if err != nil {
			t.Fatal(err)
		}
		want := `[{"id":"m2","sender":"Alice","body":"hello, \"deploy\"\nplease"}]`
		if out != want {
			t.Errorf("json =\n  %s\nwant\n  %s", out, want)
		}
	})

	t.Run("an empty result is a stable empty array, never null", func(t *testing.T) {
		out, err := renderMessages(nil, renderOpts{format: formatJSON})
		if err != nil {
			t.Fatal(err)
		}
		if out != "[]" {
			t.Errorf("json = %q, want []", out)
		}
	})

	t.Run("HTML characters in a body are not escaped into unreadable noise", func(t *testing.T) {
		out, err := renderMessages(
			[]*Message{{ID: "x", Body: "a <b> & c", MessageType: "Text"}},
			renderOpts{format: formatJSON, fields: []string{"body"}})
		if err != nil {
			t.Fatal(err)
		}
		if want := `[{"body":"a <b> & c"}]`; out != want {
			t.Errorf("json = %s, want %s (literal characters, not \\u escapes)", out, want)
		}
	})

	t.Run("jsonl is one object per line", func(t *testing.T) {
		out, err := renderMessages([]*Message{m, m}, renderOpts{
			format: formatJSONL, fields: []string{"id"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if want := "{\"id\":\"m2\"}\n{\"id\":\"m2\"}"; out != want {
			t.Errorf("jsonl = %q, want %q", out, want)
		}
	})

	t.Run("jsonl of nothing is the empty string, not a blank line", func(t *testing.T) {
		out, err := renderMessages(nil, renderOpts{format: formatJSONL})
		if err != nil {
			t.Fatal(err)
		}
		if out != "" {
			t.Errorf("jsonl = %q, want empty", out)
		}
	})

	t.Run("raw is carried through as JSON, not as a quoted string", func(t *testing.T) {
		out, err := renderMessages([]*Message{m}, renderOpts{format: formatJSON, raw: true})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, `"raw":{"secret":"x"}`) {
			t.Errorf("json = %s, want an embedded raw object", out)
		}
	})
}

func TestRenderMessagesCSV(t *testing.T) {
	m := sampleMessage()

	t.Run("cells with commas, quotes or newlines are quoted and escaped", func(t *testing.T) {
		out, err := renderMessages([]*Message{m}, renderOpts{
			format: formatCSV, fields: []string{"id", "body"},
		})
		if err != nil {
			t.Fatal(err)
		}
		want := "id,body\n" + `m2,"hello, ""deploy""` + "\n" + `please"`
		if out != want {
			t.Errorf("csv =\n%q\nwant\n%q", out, want)
		}
	})

	t.Run("a header row is always present, even with no data", func(t *testing.T) {
		out, err := renderMessages(nil, renderOpts{format: formatCSV, fields: []string{"id", "body"}})
		if err != nil {
			t.Fatal(err)
		}
		if out != "id,body" {
			t.Errorf("csv = %q, want just the header", out)
		}
	})

	t.Run("without --fields the documented default columns are used", func(t *testing.T) {
		out, err := renderMessages([]*Message{m}, renderOpts{format: formatCSV})
		if err != nil {
			t.Fatal(err)
		}
		header := strings.SplitN(out, "\n", 2)[0]
		if header != strings.Join(csvDefaultFields, ",") {
			t.Errorf("header = %q, want %q", header, strings.Join(csvDefaultFields, ","))
		}
	})
}

func TestRenderMessagesText(t *testing.T) {
	t.Run("a block names when, where, who and what", func(t *testing.T) {
		m := sampleMessage()
		out, err := renderMessages([]*Message{m}, renderOpts{format: formatText})
		if err != nil {
			t.Fatal(err)
		}
		want := ToLocal(MessageTimeMS(m)) + "  Backend Team — Alice [RichText/Html]\n  " +
			strings.TrimSpace(m.Body)
		if out != want {
			t.Errorf("text =\n%q\nwant\n%q", out, want)
		}
	})

	t.Run("blocks are separated by a blank line", func(t *testing.T) {
		m := sampleMessage()
		out, err := renderMessages([]*Message{m, m}, renderOpts{format: formatText})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "\n\n") {
			t.Errorf("text =\n%s\nwant a blank line between blocks", out)
		}
	})

	t.Run("missing metadata degrades to explicit placeholders", func(t *testing.T) {
		out, err := renderMessages([]*Message{{ID: "x", Body: "hi"}}, renderOpts{format: formatText})
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"(no time)", "(unknown conversation)", "(unresolved author)"} {
			if !strings.Contains(out, want) {
				t.Errorf("text = %q, missing %q", out, want)
			}
		}
	})

	t.Run("the conversation id stands in when there is no title", func(t *testing.T) {
		out, err := renderMessages(
			[]*Message{{ID: "x", ConversationID: "19:abc", Body: "hi"}},
			renderOpts{format: formatText})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "19:abc") {
			t.Errorf("text = %q, want the conversation id", out)
		}
	})
}

func TestRenderMessagesTable(t *testing.T) {
	m := sampleMessage()

	t.Run("a header, a rule and one line per row", func(t *testing.T) {
		out, err := renderMessages([]*Message{m}, renderOpts{
			format: formatTable, fields: []string{"sender", "id"},
		})
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(out, "\n")
		if len(lines) != 3 {
			t.Fatalf("table has %d lines, want 3:\n%s", len(lines), out)
		}
		if !strings.HasPrefix(lines[0], "sender") {
			t.Errorf("header = %q", lines[0])
		}
		if strings.Trim(lines[1], "- ") != "" {
			t.Errorf("rule = %q, want only dashes and spaces", lines[1])
		}
		if !strings.HasPrefix(lines[2], "Alice") {
			t.Errorf("row = %q", lines[2])
		}
	})

	t.Run("newlines in a body are collapsed so a row stays one line", func(t *testing.T) {
		out, err := renderMessages([]*Message{m}, renderOpts{
			format: formatTable, fields: []string{"body"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(strings.Split(out, "\n")) != 3 {
			t.Errorf("table =\n%s\nwant the body on a single line", out)
		}
	})

	t.Run("an unknown format falls back to the table", func(t *testing.T) {
		out, err := renderMessages([]*Message{m}, renderOpts{format: "", fields: []string{"id"}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(out, "id") {
			t.Errorf("out = %q, want a table", out)
		}
	})

	t.Run("without --fields the documented default columns are used", func(t *testing.T) {
		out, err := renderMessages([]*Message{m}, renderOpts{format: formatTable})
		if err != nil {
			t.Fatal(err)
		}
		header := strings.SplitN(out, "\n", 2)[0]
		for _, c := range tableDefaultFields {
			if !strings.Contains(header, c) {
				t.Errorf("header %q is missing column %q", header, c)
			}
		}
	})

	t.Run("a long body is truncated with an ellipsis", func(t *testing.T) {
		long := &Message{ID: "x", MessageType: "Text", Body: strings.Repeat("a", 200)}
		out, err := renderMessages([]*Message{long}, renderOpts{
			format: formatTable, fields: []string{"body"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "…") {
			t.Errorf("table =\n%s\nwant a truncated body", out)
		}
	})
}

func TestRenderRows(t *testing.T) {
	rows := []record{
		{{"sender", "Alice"}, {"messages", 3}},
		{{"sender", "Bo"}, {"messages", 1}},
	}

	t.Run("json keeps the record's key order", func(t *testing.T) {
		out, err := renderRows(rows[:1], renderOpts{format: formatJSON}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if want := `[{"sender":"Alice","messages":3}]`; out != want {
			t.Errorf("json = %s, want %s", out, want)
		}
	})

	t.Run("columns are derived from the first row when not given", func(t *testing.T) {
		out, err := renderRows(rows, renderOpts{format: formatCSV}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(out, "sender,messages\n") {
			t.Errorf("csv =\n%s", out)
		}
	})

	t.Run("--fields overrides the columns and drops the rest", func(t *testing.T) {
		out, err := renderRows(rows, renderOpts{format: formatCSV, fields: []string{"messages"}}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if want := "messages\n3\n1"; out != want {
			t.Errorf("csv = %q, want %q", out, want)
		}
	})

	t.Run("--fields naming an absent key still emits the column", func(t *testing.T) {
		out, err := renderRows(rows[:1], renderOpts{format: formatCSV, fields: []string{"nope"}}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if want := "nope\n"; out != want {
			t.Errorf("csv = %q, want %q", out, want)
		}
	})

	t.Run("no rows and no columns still says something", func(t *testing.T) {
		out, err := renderRows(nil, renderOpts{format: formatTable}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if out != "(no rows)" {
			t.Errorf("table = %q, want %q", out, "(no rows)")
		}
	})

	t.Run("an empty aggregate is a stable empty array", func(t *testing.T) {
		out, err := renderRows(nil, renderOpts{format: formatJSON}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if out != "[]" {
			t.Errorf("json = %q, want []", out)
		}
	})
}

func TestCSVCell(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"a plain value needs no quoting", "plain", "plain"},
		{"an absent value is an empty cell", nil, ""},
		{"a comma forces quoting", "a,b", `"a,b"`},
		{"a quote is doubled", `say "hi"`, `"say ""hi"""`},
		{"a newline forces quoting", "a\nb", "\"a\nb\""},
		{"a carriage return forces quoting", "a\rb", "\"a\rb\""},
		{"an int renders as a number", 3, "3"},
		{"an int64 renders as a number", int64(9), "9"},
		{"a bool renders as true/false", true, "true"},
		{"an empty string is an empty cell", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := csvCell(tt.in); got != tt.want {
				t.Errorf("csvCell(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTruncateCell(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"no limit means no truncation", "abcdefghij", 0, "abcdefghij"},
		{"a negative limit means no truncation", "abcdefghij", -1, "abcdefghij"},
		{"shorter than the limit is untouched", "abc", 10, "abc"},
		{"exactly the limit is untouched", "abcde", 5, "abcde"},
		{"past the limit gets an ellipsis", "abcdefghij", 5, "abcd…"},
		{"multibyte runes are counted, not bytes", "日本語テキスト", 4, "日本語…"},
		{"an emoji is one rune", "🙂🙂🙂🙂", 2, "🙂…"},
		{"an empty string stays empty", "", 5, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateCell(tt.in, tt.max); got != tt.want {
				t.Errorf("truncateCell(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
			}
		})
	}
}

func TestNullable(t *testing.T) {
	if got := nullable(""); got != nil {
		t.Errorf("nullable(\"\") = %v, want nil", got)
	}
	if got := nullable("x"); got != any("x") {
		t.Errorf("nullable(\"x\") = %v, want x", got)
	}
}

func TestRecordMarshalJSON(t *testing.T) {
	t.Run("keys keep their insertion order, unlike a map", func(t *testing.T) {
		r := record{{"z", 1}, {"a", 2}, {"m", 3}}
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if want := `{"z":1,"a":2,"m":3}`; string(b) != want {
			t.Errorf("json = %s, want %s", b, want)
		}
	})

	t.Run("an empty record is an empty object", func(t *testing.T) {
		b, err := json.Marshal(record{})
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != "{}" {
			t.Errorf("json = %s, want {}", b)
		}
	})

	t.Run("get returns nil for an absent key", func(t *testing.T) {
		if got := (record{{"a", 1}}).get("b"); got != nil {
			t.Errorf("get(b) = %v, want nil", got)
		}
	})
}
