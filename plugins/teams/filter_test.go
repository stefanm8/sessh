package teams

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestIsSystemMessage(t *testing.T) {
	tests := []struct {
		name        string
		messageType string
		want        bool
	}{
		{"a rich text message is conversation", "RichText/Html", false},
		{"a plain text message is conversation", "Text", false},
		{"a member added event is churn", "ThreadActivity/AddMember", true},
		{"a member deleted event is churn", "ThreadActivity/DeleteMember", true},
		{"a call event is churn", "Event/Call", true},
		{"the prefix match is case-insensitive", "threadactivity/addmember", true},
		{"a type that merely contains the word is not a system type", "My/ThreadActivity", false},
		{"an empty message type is conversation", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Message{MessageType: FlexString(tt.messageType)}
			if got := isSystemMessage(m); got != tt.want {
				t.Errorf("isSystemMessage(%q) = %v, want %v", tt.messageType, got, tt.want)
			}
		})
	}
}

func TestCompileGrep(t *testing.T) {
	t.Run("no patterns compile to nothing", func(t *testing.T) {
		if got := compileGrep(nil); got != nil {
			t.Errorf("compileGrep(nil) = %v, want nil", got)
		}
	})

	tests := []struct {
		name    string
		pattern string
		subject string
		want    bool
	}{
		{"a literal matches", "deploy", "we deploy tonight", true},
		{"matching is case-insensitive", "DEPLOY", "we deploy tonight", true},
		{"a real regex works", "depl(oy|oyment)", "deployment done", true},
		{"an anchor works", "^deploy", "deploy now", true},
		{"a non-match is a non-match", "rollback", "we deploy tonight", false},
		// An invalid or RE2-unsupported pattern must degrade to a literal search
		// rather than failing the query.
		{"an unbalanced paren degrades to a literal", "deploy(", "deploy( now", true},
		{"an unbalanced paren no longer matches as a regex", "deploy(", "deploy now", false},
		{"a JS-only lookahead degrades to a literal", "(?=deploy)", "text (?=deploy) here", true},
		{"a stray metachar degrades to a literal", "*hi", "say *hi there", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := compileGrep([]string{tt.pattern})
			if len(res) != 1 {
				t.Fatalf("compileGrep returned %d patterns, want 1", len(res))
			}
			if got := res[0].MatchString(tt.subject); got != tt.want {
				t.Errorf("compileGrep(%q).MatchString(%q) = %v, want %v",
					tt.pattern, tt.subject, got, tt.want)
			}
		})
	}
}

func testDirectory(convs ...*Conversation) *Directory {
	d := &Directory{Map: map[string]*Conversation{}, Present: true}
	for _, c := range convs {
		d.Map[c.ID] = c
	}
	return d
}

func TestResolveConversations(t *testing.T) {
	dir := testDirectory(
		&Conversation{ID: "19:aaa@thread.v2", Title: "Backend Team", ChannelName: "Backend", TeamName: "Engineering"},
		&Conversation{ID: "19:bbb@thread.v2", Title: "Design Sync", Members: []string{"Alex Example"}},
		&Conversation{ID: "19:ccc@thread.v2", Title: "Backend Standup"},
	)

	t.Run("no terms means no filtering at all", func(t *testing.T) {
		test, err := resolveConversations(nil, dir)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if test != nil {
			t.Error("resolveConversations(nil) should return a nil matcher, not a match-everything one")
		}
	})

	t.Run("an exact id is never ambiguous", func(t *testing.T) {
		test, err := resolveConversations([]string{"19:aaa@thread.v2"}, dir)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if !test("19:aaa@thread.v2") {
			t.Error("the exact id should match")
		}
		if test("19:bbb@thread.v2") {
			t.Error("a different id should not match")
		}
	})

	t.Run("a unique name resolves to its id", func(t *testing.T) {
		test, err := resolveConversations([]string{"Design"}, dir)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if !test("19:bbb@thread.v2") || test("19:aaa@thread.v2") {
			t.Error("Design should resolve to exactly the Design Sync conversation")
		}
	})

	t.Run("names match case-insensitively", func(t *testing.T) {
		if _, err := resolveConversations([]string{"design sync"}, dir); err != nil {
			t.Errorf("error = %v", err)
		}
	})

	t.Run("a member name is searchable", func(t *testing.T) {
		test, err := resolveConversations([]string{"Alex Example"}, dir)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if !test("19:bbb@thread.v2") {
			t.Error("a member name should resolve to their chat")
		}
	})

	t.Run("a channel or team name is searchable", func(t *testing.T) {
		if _, err := resolveConversations([]string{"Engineering"}, dir); err != nil {
			t.Errorf("error = %v", err)
		}
	})

	t.Run("an ambiguous term is a usage error listing the candidates", func(t *testing.T) {
		_, err := resolveConversations([]string{"Backend"}, dir)
		if err == nil {
			t.Fatal("want an error rather than a guess")
		}
		msg := err.Error()
		for _, want := range []string{"ambiguous", "19:aaa@thread.v2", "19:ccc@thread.v2"} {
			if !strings.Contains(msg, want) {
				t.Errorf("error %q does not mention %q", msg, want)
			}
		}
	})

	t.Run("the candidate list is ordered deterministically across runs", func(t *testing.T) {
		var first string
		for i := 0; i < 20; i++ {
			_, err := resolveConversations([]string{"Backend"}, dir)
			if err == nil {
				t.Fatal("want an error")
			}
			if i == 0 {
				first = err.Error()
			} else if err.Error() != first {
				t.Fatalf("the error text changed between runs:\n%s\nvs\n%s", first, err.Error())
			}
		}
	})

	t.Run("a term matching nothing is a usage error", func(t *testing.T) {
		_, err := resolveConversations([]string{"nowhere"}, dir)
		if err == nil {
			t.Fatal("want an error")
		}
		if !strings.Contains(err.Error(), "no conversation matches") {
			t.Errorf("error = %q", err)
		}
	})

	t.Run("an unseen raw id still filters by prefix", func(t *testing.T) {
		test, err := resolveConversations([]string{"19:zzz"}, dir)
		if err != nil {
			t.Fatalf("error = %v, want the raw id to be accepted", err)
		}
		if !test("19:zzz@thread.v2") {
			t.Error("the prefix should match a conversation the sidecar has never seen")
		}
	})

	t.Run("an id prefix resolves when it is unambiguous", func(t *testing.T) {
		test, err := resolveConversations([]string{"19:aaa"}, dir)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if !test("19:aaa@thread.v2") {
			t.Error("an id prefix should match")
		}
	})

	t.Run("several terms are ORed together", func(t *testing.T) {
		test, err := resolveConversations([]string{"Design", "19:ccc@thread.v2"}, dir)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if !test("19:bbb@thread.v2") || !test("19:ccc@thread.v2") {
			t.Error("both terms should match")
		}
		if test("19:aaa@thread.v2") {
			t.Error("an unnamed conversation should not match")
		}
	})

	t.Run("without a sidecar only ids and prefixes are available", func(t *testing.T) {
		for _, d := range []*Directory{nil, {Map: map[string]*Conversation{}}} {
			test, err := resolveConversations([]string{"19:aaa"}, d)
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if !test("19:aaa@thread.v2") {
				t.Error("prefix matching should still work with no sidecar")
			}
			if test("19:bbb@thread.v2") {
				t.Error("a different id should not match")
			}
		}
	})
}

func TestCandidateList(t *testing.T) {
	t.Run("names are shown beside their ids", func(t *testing.T) {
		got := candidateList([]*Conversation{
			{ID: "19:a", Title: "Backend Team", DisplayName: "Backend Team"},
			{ID: "19:b"},
		})
		if !strings.Contains(got, "19:a") || !strings.Contains(got, "(Backend Team)") {
			t.Errorf("candidateList() = %q", got)
		}
		if strings.Contains(got, "19:b  (") {
			t.Errorf("a nameless conversation should not get empty parentheses: %q", got)
		}
	})

	t.Run("a long list is elided rather than flooding the terminal", func(t *testing.T) {
		var hits []*Conversation
		for i := 0; i < 20; i++ {
			hits = append(hits, &Conversation{ID: string(rune('a' + i))})
		}
		got := candidateList(hits)
		if !strings.HasSuffix(got, "…") {
			t.Errorf("candidateList() should end with an ellipsis, got %q", got)
		}
		if n := strings.Count(got, "  - "); n != 8 {
			t.Errorf("candidateList() listed %d candidates, want 8 before the ellipsis", n)
		}
	})
}

func TestSearchableText(t *testing.T) {
	t.Run("every name and member is searchable", func(t *testing.T) {
		got := searchableText(&Conversation{
			Title: "T", DisplayName: "D", ShortTitle: "S", LongTitle: "L",
			ChannelName: "C", TeamName: "M", Members: []string{"Alex", "Blair"},
		})
		for _, want := range []string{"T", "D", "S", "L", "C", "M", "Alex", "Blair"} {
			if !strings.Contains(got, want) {
				t.Errorf("searchableText() = %q, missing %q", got, want)
			}
		}
	})
	t.Run("empty fields do not create blank lines that match everything", func(t *testing.T) {
		got := searchableText(&Conversation{Title: "T"})
		if strings.Contains(got, "\n\n") || strings.HasPrefix(got, "\n") {
			t.Errorf("searchableText() = %q, want no empty entries", got)
		}
	})
}

func msg(id, conv, sender, body string) *Message {
	return &Message{ID: id, ConversationID: conv, Sender: sender, Body: body,
		MessageType: "RichText/Html", ComposeTime: "1700000100000", Source: "idb"}
}

func TestBuildPredicate(t *testing.T) {
	t.Run("system messages are dropped by default", func(t *testing.T) {
		keep := buildPredicate(filterSpec{})
		if keep(&Message{MessageType: "ThreadActivity/AddMember"}) {
			t.Error("membership churn should be filtered out by default")
		}
		if !keep(&Message{MessageType: "RichText/Html"}) {
			t.Error("a real message should be kept")
		}
	})

	t.Run("system messages are kept when explicitly included", func(t *testing.T) {
		keep := buildPredicate(filterSpec{includeSystem: true})
		if !keep(&Message{MessageType: "ThreadActivity/AddMember"}) {
			t.Error("--include-system should keep churn")
		}
	})

	t.Run("a conversation test is applied", func(t *testing.T) {
		keep := buildPredicate(filterSpec{convTest: func(id string) bool { return id == "c1" }})
		if !keep(msg("1", "c1", "Alex", "x")) || keep(msg("2", "c2", "Alex", "x")) {
			t.Error("only the named conversation should survive")
		}
	})

	t.Run("senders OR within the flag and match as a substring", func(t *testing.T) {
		keep := buildPredicate(filterSpec{senders: []string{"alex", "blair"}})
		cases := map[string]bool{
			"Alex Example":     true,
			"Blair Example":    true,
			"PrefixAlexSuffix": true, // match within a word, not only at name/word boundaries
			"Casey Example":    false,
		}
		for sender, want := range cases {
			if got := keep(msg("1", "c", sender, "x")); got != want {
				t.Errorf("sender %q kept = %v, want %v", sender, got, want)
			}
		}
	})

	t.Run("sender filtering also matches the stable MRI", func(t *testing.T) {
		keep := buildPredicate(filterSpec{senders: []string{"00000001"}})
		m := msg("1", "c", "Alex Example", "x")
		m.AuthorMRI = "8:orgid:00000001-0000-4000-8000-000000000001"
		if !keep(m) {
			t.Error("an MRI substring should match independently of the display name")
		}
	})

	t.Run("a since bound is inclusive and an until bound is exclusive", func(t *testing.T) {
		const at = int64(1700000100000)
		m := &Message{ComposeTime: FlexString("1700000100000"), MessageType: "Text"}
		cases := []struct {
			name string
			spec filterSpec
			want bool
		}{
			{"exactly at since is inside", filterSpec{hasSince: true, sinceMS: at}, true},
			{"one ms before since is outside", filterSpec{hasSince: true, sinceMS: at + 1}, false},
			{"exactly at until is outside", filterSpec{hasUntil: true, untilMS: at}, false},
			{"one ms before until is inside", filterSpec{hasUntil: true, untilMS: at + 1}, true},
			{"inside both bounds", filterSpec{hasSince: true, sinceMS: at - 1, hasUntil: true, untilMS: at + 1}, true},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				if got := buildPredicate(c.spec)(m); got != c.want {
					t.Errorf("kept = %v, want %v", got, c.want)
				}
			})
		}
	})

	t.Run("a message with no time cannot satisfy a bound", func(t *testing.T) {
		undated := &Message{MessageType: "Text"}
		if buildPredicate(filterSpec{hasSince: true, sinceMS: 0})(undated) {
			t.Error("an undated message must not slip through a --since filter")
		}
		if !buildPredicate(filterSpec{})(undated) {
			t.Error("an undated message should survive when no bound is set")
		}
	})

	t.Run("types are an exact-match set", func(t *testing.T) {
		keep := buildPredicate(filterSpec{
			types:         map[string]struct{}{"Text": {}},
			includeSystem: true,
		})
		if !keep(&Message{MessageType: "Text"}) {
			t.Error("the listed type should be kept")
		}
		if keep(&Message{MessageType: "RichText/Html"}) {
			t.Error("an unlisted type should be dropped")
		}
	})

	t.Run("repeated greps AND, because a second pattern narrows", func(t *testing.T) {
		keep := buildPredicate(filterSpec{grep: compileGrep([]string{"deploy", "friday"})})
		if !keep(msg("1", "c", "Alex", "deploy on friday")) {
			t.Error("a body matching both patterns should be kept")
		}
		if keep(msg("2", "c", "Alex", "deploy on monday")) {
			t.Error("a body matching only one pattern should be dropped")
		}
	})

	t.Run("grep matches the body, not the metadata", func(t *testing.T) {
		keep := buildPredicate(filterSpec{grep: compileGrep([]string{"Alex"})})
		if keep(msg("1", "c", "Alex", "nothing relevant")) {
			t.Error("--grep must not match on the sender name")
		}
	})

	t.Run("a capture source is matched exactly", func(t *testing.T) {
		keep := buildPredicate(filterSpec{captureSource: "idb"})
		if !keep(&Message{Source: "idb", MessageType: "Text"}) {
			t.Error("the named source should be kept")
		}
		if keep(&Message{Source: "ws", MessageType: "Text"}) {
			t.Error("another source should be dropped")
		}
	})

	t.Run("different filters AND across each other", func(t *testing.T) {
		keep := buildPredicate(filterSpec{
			senders: []string{"alex"},
			grep:    compileGrep([]string{"deploy"}),
		})
		if !keep(msg("1", "c", "Alex", "deploy")) {
			t.Error("a row satisfying both filters should be kept")
		}
		if keep(msg("2", "c", "Blair", "deploy")) {
			t.Error("failing the sender filter should drop the row")
		}
		if keep(msg("3", "c", "Alex", "lunch")) {
			t.Error("failing the grep filter should drop the row")
		}
	})
}

// newFilterCmd wires a queryFlags to a command the way the real query commands
// do, so Flags().Changed reflects what the caller actually passed.
func newFilterCmd(t *testing.T, args ...string) (*queryFlags, *cobra.Command) {
	t.Helper()
	f := &queryFlags{}
	cmd := &cobra.Command{Use: "test", RunE: func(*cobra.Command, []string) error { return nil }}
	f.bindFilter(cmd)
	if err := cmd.Flags().Parse(args); err != nil {
		t.Fatalf("parsing %v: %v", args, err)
	}
	return f, cmd
}

func TestBuildFilter(t *testing.T) {
	clock := time.Date(2023, 11, 15, 12, 0, 0, 0, time.UTC)

	t.Run("an unparseable --since is a usage error, not an empty result", func(t *testing.T) {
		f, cmd := newFilterCmd(t, "--since", "not-a-date")
		if _, err := f.buildFilter(cmd, nil, clock); err == nil {
			t.Fatal("want an error")
		}
	})

	t.Run("an unparseable --until is a usage error", func(t *testing.T) {
		f, cmd := newFilterCmd(t, "--until", "nope")
		if _, err := f.buildFilter(cmd, nil, clock); err == nil {
			t.Fatal("want an error")
		}
	})

	t.Run("bounds are only set when the flag was actually passed", func(t *testing.T) {
		f, cmd := newFilterCmd(t)
		spec, err := f.buildFilter(cmd, nil, clock)
		if err != nil {
			t.Fatal(err)
		}
		if spec.hasSince || spec.hasUntil {
			t.Errorf("spec has bounds without the flags being passed: %+v", spec)
		}
	})

	t.Run("a passed --since is resolved against the injected clock", func(t *testing.T) {
		f, cmd := newFilterCmd(t, "--since", "2h")
		spec, err := f.buildFilter(cmd, nil, clock)
		if err != nil {
			t.Fatal(err)
		}
		if want := clock.UnixMilli() - 2*3_600_000; !spec.hasSince || spec.sinceMS != want {
			t.Errorf("sinceMS = %d (set %v), want %d", spec.sinceMS, spec.hasSince, want)
		}
	})

	t.Run("an unknown --capture-source is rejected and the valid ones are listed", func(t *testing.T) {
		f, cmd := newFilterCmd(t, "--capture-source", "carrier-pigeon")
		_, err := f.buildFilter(cmd, nil, clock)
		if err == nil {
			t.Fatal("want an error")
		}
		for _, want := range []string{"idb", "ws", "rest"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not list %q", err, want)
			}
		}
	})

	t.Run("a capture source is normalized to lower case", func(t *testing.T) {
		f, cmd := newFilterCmd(t, "--capture-source", "IDB")
		spec, err := f.buildFilter(cmd, nil, clock)
		if err != nil {
			t.Fatal(err)
		}
		if spec.captureSource != "idb" {
			t.Errorf("captureSource = %q, want idb", spec.captureSource)
		}
	})

	t.Run("senders are lower-cased so matching is case-insensitive", func(t *testing.T) {
		f, cmd := newFilterCmd(t, "--sender", "Alex", "--sender", "BLAIR")
		spec, err := f.buildFilter(cmd, nil, clock)
		if err != nil {
			t.Fatal(err)
		}
		if len(spec.senders) != 2 || spec.senders[0] != "alex" || spec.senders[1] != "blair" {
			t.Errorf("senders = %v, want [alex blair]", spec.senders)
		}
	})

	t.Run("system messages are excluded by default", func(t *testing.T) {
		f, cmd := newFilterCmd(t)
		spec, err := f.buildFilter(cmd, nil, clock)
		if err != nil {
			t.Fatal(err)
		}
		if spec.includeSystem {
			t.Error("includeSystem should be false by default")
		}
	})

	t.Run("--include-system opts back in", func(t *testing.T) {
		f, cmd := newFilterCmd(t, "--include-system")
		spec, err := f.buildFilter(cmd, nil, clock)
		if err != nil {
			t.Fatal(err)
		}
		if !spec.includeSystem {
			t.Error("--include-system should include system messages")
		}
	})

	t.Run("--only-messages=false opts back in", func(t *testing.T) {
		f, cmd := newFilterCmd(t, "--only-messages=false")
		spec, err := f.buildFilter(cmd, nil, clock)
		if err != nil {
			t.Fatal(err)
		}
		if !spec.includeSystem {
			t.Error("explicitly turning --only-messages off should include system messages")
		}
	})

	t.Run("an explicit system --type wins over the default filter", func(t *testing.T) {
		// The footgun this guards: asking for ThreadActivity/AddMember while the
		// default --only-messages silently drops exactly that type.
		f, cmd := newFilterCmd(t, "--type", "ThreadActivity/AddMember")
		spec, err := f.buildFilter(cmd, nil, clock)
		if err != nil {
			t.Fatal(err)
		}
		if !spec.includeSystem {
			t.Fatal("asking for a system type should include system messages")
		}
		keep := buildPredicate(spec)
		if !keep(&Message{MessageType: "ThreadActivity/AddMember"}) {
			t.Error("the explicitly requested type should survive the filter")
		}
	})

	t.Run("a non-system --type does not re-enable system messages", func(t *testing.T) {
		f, cmd := newFilterCmd(t, "--type", "Text")
		spec, err := f.buildFilter(cmd, nil, clock)
		if err != nil {
			t.Fatal(err)
		}
		if spec.includeSystem {
			t.Error("a normal --type should leave the system filter alone")
		}
	})

	t.Run("an unresolvable --conversation is a usage error", func(t *testing.T) {
		dir := testDirectory(&Conversation{ID: "19:a", Title: "Backend"})
		f, cmd := newFilterCmd(t, "--conversation", "nowhere")
		if _, err := f.buildFilter(cmd, dir, clock); err == nil {
			t.Fatal("want an error")
		}
	})
}
