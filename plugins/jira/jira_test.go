package jira

import (
	"encoding/json"
	"testing"
)

func TestBuildJQL(t *testing.T) {
	tests := []struct {
		name     string
		project  string
		status   string
		assignee string
		rawJQL   string
		want     string
	}{
		{
			name:    "a bare project is bounded and ordered",
			project: "ENG",
			want:    `project = "ENG" ORDER BY updated DESC`,
		},
		{
			name:    "project, status and assignee compose with AND in that order",
			project: "ENG", status: "In Progress", assignee: "me",
			want: `project = "ENG" AND status = "In Progress" AND assignee = currentUser() ORDER BY updated DESC`,
		},
		{
			name:   "a status filter alone still bounds the query",
			status: "Done",
			want:   `status = "Done" ORDER BY updated DESC`,
		},
		{
			name:     "a named assignee is quoted verbatim",
			assignee: "alice@example.com",
			want:     `assignee = "alice@example.com" ORDER BY updated DESC`,
		},
		{
			name:    "project and status without an assignee",
			project: "ENG", status: "To Do",
			want: `project = "ENG" AND status = "To Do" ORDER BY updated DESC`,
		},
		{
			name: "with no filters at all only the ordering remains",
			want: "ORDER BY updated DESC",
		},
		{
			name:    "an explicit --jql replaces everything else",
			project: "ENG", status: "Done", assignee: "me",
			rawJQL: "assignee = currentUser() AND sprint in openSprints()",
			want:   "assignee = currentUser() AND sprint in openSprints()",
		},
		{
			name:   "a raw query is passed through untouched, ordering included",
			rawJQL: "key = ENG-1",
			want:   "key = ENG-1",
		},
		{
			name:   "quotes in a status are escaped so the query stays valid",
			status: `Wait "Now"`,
			want:   `status = "Wait \"Now\"" ORDER BY updated DESC`,
		},
		{
			name:    "quotes in a project are escaped too",
			project: `A"B`,
			want:    `project = "A\"B" ORDER BY updated DESC`,
		},
		{
			name:     "quotes in an assignee are escaped too",
			assignee: `a"b`,
			want:     `assignee = "a\"b" ORDER BY updated DESC`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildSearchJQL(tt.project, tt.status, tt.assignee, tt.rawJQL, searchFilters{})
			if got != tt.want {
				t.Errorf("BuildJQL(%q, %q, %q, %q) =\n  %q\nwant\n  %q",
					tt.project, tt.status, tt.assignee, tt.rawJQL, got, tt.want)
			}
		})
	}
}

func TestBuildJQLMeAliases(t *testing.T) {
	currentUser := `assignee = currentUser() ORDER BY updated DESC`

	tests := []struct {
		name     string
		assignee string
		want     string
	}{
		{"me", "me", currentUser},
		{"@me", "@me", currentUser},
		{"current", "current", currentUser},
		{"currentuser", "currentuser", currentUser},
		{"upper case ME", "ME", currentUser},
		{"mixed case CurrentUser", "CurrentUser", currentUser},
		// Near-misses: anything else is a literal name, never currentUser().
		{"a user literally named mel", "mel", `assignee = "mel" ORDER BY updated DESC`},
		{"@current is not an alias", "@current", `assignee = "@current" ORDER BY updated DESC`},
		{"me with surrounding spaces is a literal", " me ", `assignee = " me " ORDER BY updated DESC`},
		{"an account id is a literal", "example-account-id", `assignee = "example-account-id" ORDER BY updated DESC`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildSearchJQL("", "", tt.assignee, "", searchFilters{}); got != tt.want {
				t.Errorf("BuildJQL(assignee=%q) = %q, want %q", tt.assignee, got, tt.want)
			}
		})
	}
}

func TestEscapeJQL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"nothing to escape", "In Progress", "In Progress"},
		{"one quote", `a"b`, `a\"b`},
		{"several quotes", `"a" "b"`, `\"a\" \"b\"`},
		{"empty", "", ""},
		{"a backslash is escaped", `a\b`, `a\\b`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := escapeJQL(tt.in); got != tt.want {
				t.Errorf("escapeJQL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// adf decodes a JSON literal the way the API layer does, so these tests feed
// adfToText exactly the shapes it sees in production.
func adf(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("bad ADF fixture: %v", err)
	}
	return v
}

func TestADFToText(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want string
	}{
		{
			name: "a single paragraph ends with a newline",
			doc:  `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Hello"}]}]}`,
			want: "Hello\n",
		},
		{
			name: "sibling text nodes are concatenated without a separator",
			doc:  `{"type":"paragraph","content":[{"type":"text","text":"Hello "},{"type":"text","text":"world"}]}`,
			want: "Hello world\n",
		},
		{
			name: "paragraphs are separated by their trailing newlines",
			doc: `{"type":"doc","content":[
				{"type":"paragraph","content":[{"type":"text","text":"Hello world"}]},
				{"type":"paragraph","content":[{"type":"text","text":"Second line"}]}]}`,
			want: "Hello world\nSecond line\n",
		},
		{
			name: "a hardBreak becomes a newline inside a paragraph",
			doc: `{"type":"paragraph","content":[
				{"type":"text","text":"one"},{"type":"hardBreak"},{"type":"text","text":"two"}]}`,
			want: "one\ntwo\n",
		},
		{
			name: "a heading is a block",
			doc:  `{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Title"}]}`,
			want: "Title\n",
		},
		{
			name: "a codeBlock is a block",
			doc:  `{"type":"codeBlock","attrs":{"language":"go"},"content":[{"type":"text","text":"x := 1"}]}`,
			want: "x := 1\n",
		},
		{
			name: "a blockquote is a block and keeps its inner paragraph break",
			doc:  `{"type":"blockquote","content":[{"type":"paragraph","content":[{"type":"text","text":"quoted"}]}]}`,
			want: "quoted\n\n",
		},
		{
			name: "list items get a dash prefix",
			doc: `{"type":"bulletList","content":[
				{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"one"}]}]},
				{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"two"}]}]}]}`,
			want: "- one\n\n- two\n\n",
		},
		{
			name: "an ordered list uses the same dash prefix",
			doc: `{"type":"orderedList","content":[
				{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"first"}]}]}]}`,
			want: "- first\n\n",
		},
		{
			name: "marks on a text node do not change its text",
			doc:  `{"type":"paragraph","content":[{"type":"text","text":"bold","marks":[{"type":"strong"}]}]}`,
			want: "bold\n",
		},
		{
			name: "an unknown node type still contributes its children",
			doc:  `{"type":"mysteryPanel","content":[{"type":"text","text":"kept"}]}`,
			want: "kept",
		},
		{
			name: "a mention contributes its display text",
			doc:  `{"type":"mention","attrs":{"id":"abc","text":"@Example User"}}`,
			want: "@Example User",
		},
		{
			name: "a text node whose text is not a string is skipped, not fatal",
			doc:  `{"type":"paragraph","content":[{"type":"text","text":42},{"type":"text","text":"ok"}]}`,
			want: "ok\n",
		},
		{
			name: "deeply nested content is flattened",
			doc: `{"type":"doc","content":[{"type":"bulletList","content":[
				{"type":"listItem","content":[
					{"type":"paragraph","content":[{"type":"text","text":"outer"}]},
					{"type":"bulletList","content":[
						{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"inner"}]}]}]}]}]}]}`,
			want: "- outer\n- inner\n\n\n",
		},
		{
			name: "an empty doc yields nothing",
			doc:  `{"type":"doc","content":[]}`,
			want: "",
		},
		{
			name: "a bare array of nodes is walked",
			doc:  `[{"type":"paragraph","content":[{"type":"text","text":"a"}]},{"type":"paragraph","content":[{"type":"text","text":"b"}]}]`,
			want: "a\nb\n",
		},
		{
			name: "a plain string is its own text",
			doc:  `"legacy plain text"`,
			want: "legacy plain text",
		},
		{
			name: "JSON null yields nothing",
			doc:  `null`,
			want: "",
		},
		{
			name: "a number is not text and yields nothing",
			doc:  `42`,
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := adfToText(adf(t, tt.doc)); got != tt.want {
				t.Errorf("adfToText() = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("a nil interface is handled without panicking", func(t *testing.T) {
		if got := adfToText(nil); got != "" {
			t.Errorf("adfToText(nil) = %q, want empty", got)
		}
	})
}

func TestText(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want string
	}{
		{
			name: "an ADF document is rendered and trimmed",
			doc: `{"type":"doc","content":[
				{"type":"paragraph","content":[{"type":"text","text":"Hello world"}]},
				{"type":"paragraph","content":[{"type":"text","text":"Second line"}]}]}`,
			want: "Hello world\nSecond line",
		},
		{
			name: "a plain string field (older APIs) is trimmed",
			doc:  `"  legacy description  "`,
			want: "legacy description",
		},
		{
			name: "an empty string stays empty",
			doc:  `""`,
			want: "",
		},
		{
			name: "a missing field is empty, not a panic",
			doc:  `null`,
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := text(adf(t, tt.doc)); got != tt.want {
				t.Errorf("text() = %q, want %q", got, tt.want)
			}
		})
	}
}
