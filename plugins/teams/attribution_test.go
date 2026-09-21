package teams

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestConversationIDFallbacks(t *testing.T) {
	tests := []struct {
		name, want, source string
		raw                map[string]any
	}{
		{"direct field wins", "19:direct@thread.v2", "direct", map[string]any{
			"conversationId": "19:direct@thread.v2", "originalThreadId": "19:other@thread.v2"}},
		{"original thread id", "19:devops@thread.v2", "originalThreadId", map[string]any{
			"originalThreadId": "19:devops@thread.v2"}},
		{"conversation link", "19:chat@thread.v2", "conversationLink", map[string]any{
			"conversationLink": "conversation/19:chat@thread.v2;messageid=123"}},
		{"virtual conversation link", "48:mentions", "conversationLink", map[string]any{
			"conversationLink": "conversation/48:mentions;messageid=123"}},
		{"undefined link is unresolved", "", "", map[string]any{
			"conversationLink": "conversation/undefined;messageid=undefined"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, source := conversationIDOf(tt.raw)
			if got != tt.want || source != tt.source {
				t.Fatalf("conversationIDOf() = (%q, %q), want (%q, %q)", got, source, tt.want, tt.source)
			}
		})
	}
}

func TestAuthorAttribution(t *testing.T) {
	resolver := newIdentityResolver()
	resolver.observe("8:orgid:alex", "Alex Example")
	resolver.observe("8:orgid:blair", "Blair Example")
	tests := []struct {
		name, messageType             string
		raw                           map[string]any
		wantMRI, wantName, wantStatus string
	}{
		{
			name:    "worker transport prefix is identity not a display name",
			raw:     map[string]any{"from": "worker/8:orgid:alex"},
			wantMRI: "8:orgid:alex", wantName: "Alex Example", wantStatus: authorResolved,
		},
		{
			name:    "bare orgid pseudo-name is canonicalized and left unresolved",
			raw:     map[string]any{"creator": "8:orgid:unknown", "imDisplayName": "orgid:unknown"},
			wantMRI: "8:orgid:unknown", wantStatus: authorUnresolved,
		},
		{
			name:    "original sender resolves alternate IDB shape",
			raw:     map[string]any{"originalSender": "8:orgid:blair"},
			wantMRI: "8:orgid:blair", wantName: "Blair Example", wantStatus: authorResolved,
		},
		{
			name:        "event creator can be a person",
			messageType: "Event/Call", raw: map[string]any{"creator": "8:orgid:alex"},
			wantMRI: "8:orgid:alex", wantName: "Alex Example", wantStatus: authorResolved,
		},
		{
			name:        "thread activity creator is a conversation not a person",
			messageType: "ThreadActivity/AddMember", raw: map[string]any{"creator": "19:thread@thread.v2"},
			wantStatus: authorSystem,
		},
		{
			name:    "application MRI is stable too",
			raw:     map[string]any{"creator": "28:bot-id", "imDisplayName": "Jira Cloud"},
			wantMRI: "28:bot-id", wantName: "Jira Cloud", wantStatus: authorResolved,
		},
		{
			name:       "worker undefined is neither a person nor a name",
			raw:        map[string]any{"from": "worker/undefined"},
			wantStatus: authorUnresolved,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Message{ID: "m", MessageType: FlexString(tt.messageType)}
			normalizeMessageAttribution(m, tt.raw, resolver)
			if m.AuthorMRI != tt.wantMRI || m.Sender != tt.wantName || m.AuthorStatus != tt.wantStatus {
				t.Fatalf("got MRI=%q sender=%q status=%q; want MRI=%q sender=%q status=%q",
					m.AuthorMRI, m.Sender, m.AuthorStatus, tt.wantMRI, tt.wantName, tt.wantStatus)
			}
		})
	}
}

func TestTypedCallParticipantIdentityResolution(t *testing.T) {
	resolver := newIdentityResolver()
	raw := map[string]any{
		"contentType": "application/user+xml",
		"content": `<ended/><partlist><part identity="8:orgid:casey"><name>8:orgid:casey</name>` +
			`<displayName>Casey Example</displayName></part></partlist>`,
		"creator": "8:orgid:casey",
	}
	m := &Message{ID: "call", MessageType: "Event/Call"}
	normalizeMessageAttribution(m, raw, resolver)
	if m.AuthorMRI != "8:orgid:casey" || m.Sender != "Casey Example" || m.AuthorStatus != authorResolved {
		t.Fatalf("typed call identity was not resolved: %+v", m)
	}

	// The same words in ordinary message text are not a trusted directory.
	plain := map[string]any{"contentType": "Text", "content": raw["content"], "creator": "8:orgid:casey"}
	m = &Message{ID: "plain"}
	normalizeMessageAttribution(m, plain, newIdentityResolver())
	if m.Sender != "" || m.AuthorStatus != authorUnresolved {
		t.Fatalf("plain body text invented an identity: %+v", m)
	}
}

func TestSameNamedChannelsAreTeamQualified(t *testing.T) {
	a := &Conversation{ID: "19:a@thread.v2", Kind: "channel", ChannelName: "General", TeamName: "Alpha"}
	b := &Conversation{ID: "19:b@thread.v2", Kind: "channel", ChannelName: "General", TeamName: "Beta"}
	if a.Name() != "Alpha / General" || b.Name() != "Beta / General" {
		t.Fatalf("qualified names = %q, %q", a.Name(), b.Name())
	}
	_, err := resolveConversations([]string{"General"}, testDirectory(a, b))
	if err == nil || !strings.Contains(err.Error(), "Alpha / General") || !strings.Contains(err.Error(), "Beta / General") {
		t.Fatalf("ambiguous candidate error does not carry both team-qualified names: %v", err)
	}
}

func TestExplicitTeamRecordsResolveChannelNames(t *testing.T) {
	teams := explicitTeamNames([]json.RawMessage{json.RawMessage(`{
		"id":"19:general@thread.v2","groupId":"group-1","displayName":"Platform Engineering"}`)})
	tests := []struct {
		name, id, teamID, groupID string
	}{
		{"child channel resolves by parent thread", "19:releases@thread.v2", "19:general@thread.v2", ""},
		{"General resolves by its own thread", "19:general@thread.v2", "", ""},
		{"channel resolves by group id", "19:other@thread.v2", "", "group-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := normalizeConversation(idbConversation{
				ID: FlexString(tt.id), Type: "Topic", TeamID: FlexString(tt.teamID), GroupID: FlexString(tt.groupID),
				ThreadProperties: &struct {
					Topic            FlexString `json:"topic"`
					TopicThreadTopic FlexString `json:"topicThreadTopic"`
					ThreadType       FlexString `json:"threadType"`
				}{Topic: "Releases", ThreadType: "topic"},
			}, teams, "now")
			if channel == nil || channel.TeamName != "Platform Engineering" || channel.ChannelName != "Releases" {
				t.Fatalf("normalized channel = %+v", channel)
			}
		})
	}
}

func TestStableAuthorGroupingSurvivesRenamesAndNameCollisions(t *testing.T) {
	tests := []struct {
		name string
		rows []*Message
		want int
	}{
		{
			name: "one MRI keeps one identity across a rename",
			rows: []*Message{
				{AuthorMRI: "8:orgid:a", Sender: "Old Name", AuthorStatus: authorResolved},
				{AuthorMRI: "8:orgid:a", Sender: "New Name", AuthorStatus: authorResolved},
			},
			want: 1,
		},
		{
			name: "two MRIs remain distinct despite the same display name",
			rows: []*Message{
				{AuthorMRI: "8:orgid:a", Sender: "Alex", AuthorStatus: authorResolved},
				{AuthorMRI: "8:orgid:b", Sender: "Alex", AuthorStatus: authorResolved},
			},
			want: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groups := map[string]struct{}{}
			for _, m := range tt.rows {
				groups[authorGroupKey(m)] = struct{}{}
			}
			if len(groups) != tt.want {
				t.Fatalf("stable groups = %d, want %d", len(groups), tt.want)
			}
		})
	}
}

func TestFullConversationRosterIsNotCappedAtThree(t *testing.T) {
	var members []conversationMember
	for i := 0; i < 6; i++ {
		members = append(members, conversationMember{MRI: "8:orgid:user" + string(rune('a'+i)), DisplayName: "User " + string(rune('A'+i))})
	}
	rec := normalizeConversation(idbConversation{
		ID: "19:group@thread.v2", Type: "Chat", Members: members, MembersComplete: true,
	}, nil, "now")
	if rec == nil || len(rec.Members) != 6 || !rec.MembersComplete {
		t.Fatalf("roster = %+v, want all six complete members", rec)
	}
}

func TestDedupeUsesCanonicalTimeAndNeverContentMergesAuthorlessSystemRows(t *testing.T) {
	a := &Message{ID: "a", ConversationID: "c", AuthorMRI: "8:orgid:u", Body: "hello", ComposeTime: "1700000000000"}
	b := &Message{ID: "b", ConversationID: "c", AuthorMRI: "8:orgid:u", Body: "hello", ComposeTime: "2023-11-14T22:13:20Z"}
	if contentKey(a) == "" || contentKey(a) != contentKey(b) {
		t.Fatalf("mixed timestamp formats did not canonicalize: %q vs %q", contentKey(a), contentKey(b))
	}
	system := &Message{ID: "s", ConversationID: "c", Body: "member payload",
		MessageType: "ThreadActivity/AddMember", ComposeTime: "1700000000000",
		Raw: json.RawMessage(`{"creator":"19:c@thread.v2"}`)}
	if got := contentKey(system); got != "" {
		t.Fatalf("authorless system content key = %q, want id-only dedupe", got)
	}
}

func TestAppendOnlyAttributionBackfillIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "messages.jsonl")
	lines := []string{
		`{"id":"m1","sender":"worker/8:orgid:alex","body":"hello","messageType":"Message","composeTime":"1700000000000","raw":{"from":"worker/8:orgid:alex","conversationLink":"conversation/19:chat@thread.v2;messageid=m1"}}`,
		`{"id":"m2","body":"joined","messageType":"ThreadActivity/AddMember","composeTime":"1700000001000","raw":{"creator":"19:chat@thread.v2","conversationId":"19:chat@thread.v2"}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	conv := `{"id":"19:chat@thread.v2","title":"DevOps","members":[{"mri":"8:orgid:alex","displayName":"Alex Example"}]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "conversations.jsonl"), []byte(conv), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := runAttributionBackfill(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.RecordsChanged != 2 || report.RecordsAppended != 2 || report.ConversationFromLink != 1 ||
		report.GarbageSenderResolved != 1 || report.SystemAuthorless != 1 || report.DuplicateContentKeysAfter != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if _, err := os.Stat(report.MessagesBackup); err != nil {
		t.Fatalf("messages backup: %v", err)
	}

	var got []*Message
	res, err := StreamMessages(path, func(m *Message) { got = append(got, m) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 2 || res.Physical != 4 {
		t.Fatalf("scan = %+v", res)
	}
	if got[0].ConversationID != "19:chat@thread.v2" || got[0].AuthorMRI != "8:orgid:alex" || got[0].Sender != "Alex Example" {
		t.Fatalf("message was not repaired: %+v", got[0])
	}
	if got[1].AuthorStatus != authorSystem || got[1].AuthorMRI != "" {
		t.Fatalf("system message invented an author: %+v", got[1])
	}

	again, err := runAttributionBackfill(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if again.RecordsChanged != 0 || again.RecordsAppended != 0 || again.PhysicalRecordsAfter != 4 {
		t.Fatalf("second run was not idempotent: %+v", again)
	}
}

func TestMemberMergeRetainsStableMRIAndLatestName(t *testing.T) {
	got := mergeConversationMembers(
		[]conversationMember{{MRI: "8:orgid:u", DisplayName: "Old Name"}},
		[]conversationMember{{MRI: "8:orgid:u", DisplayName: "New Name"}, {MRI: "8:orgid:v"}},
	)
	want := []conversationMember{{MRI: "8:orgid:u", DisplayName: "New Name"}, {MRI: "8:orgid:v"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("members = %#v, want %#v", got, want)
	}
}
