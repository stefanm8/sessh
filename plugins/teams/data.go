package teams

import (
	"bufio"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/stefanm8/sessh/session"
)

// data.go is the only place that knows where the captured data lives and how to
// read it. Everything downstream is handed parsed message objects.
//
// Reads stream: messages.jsonl is millions of lines on a busy tenant and must
// never be slurped. A single malformed line is skipped and counted, never
// allowed to abort a query.

// FlexString tolerates a field that Teams sometimes sends as a number and
// sometimes as a string.
type FlexString string

func (f *FlexString) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = FlexString(s)
		return nil
	}
	*f = FlexString(strings.Trim(string(b), `"`))
	return nil
}

func (f FlexString) String() string { return string(f) }

// Message is a normalized captured message. The JSON tags are the on-disk
// contract shared with the capture writer.
type Message struct {
	ID             string          `json:"id"`
	ConversationID string          `json:"conversationId,omitempty"`
	Sender         string          `json:"sender,omitempty"`
	AuthorMRI      string          `json:"authorMRI,omitempty"`
	AuthorStatus   string          `json:"authorStatus,omitempty"`
	Body           string          `json:"body"`
	ContentType    string          `json:"contentType,omitempty"`
	MessageType    FlexString      `json:"messageType,omitempty"`
	ComposeTime    FlexString      `json:"composeTime,omitempty"`
	CapturedAt     string          `json:"capturedAt,omitempty"`
	Source         string          `json:"source,omitempty"`
	Raw            json.RawMessage `json:"raw,omitempty"`

	// Query-time enrichment from the conversation sidecar. Never written back
	// to messages.jsonl, which stays append-only.
	ConversationTitle string `json:"-"`
	ConversationType  string `json:"-"`
	ChannelName       string `json:"-"`
	TeamID            string `json:"-"`
	TeamName          string `json:"-"`
}

// DataDirDefault is where capture and query agree the data lives.
func DataDirDefault() string {
	if v := os.Getenv("TEAMS_DATA_DIR"); v != "" {
		return v
	}
	return filepath.Join(session.Root(), "teams")
}

// ResolveMessagesPath applies the documented resolution order: --source, then
// $TEAMS_MESSAGES_FILE, then the shared data-directory default. The FIRST configured
// candidate is authoritative — we never silently fall through from an explicit
// source to a lower-priority one, or `--source /typo` would quietly read the
// default file.
func ResolveMessagesPath(source string) (string, error) {
	var (
		label string
		path  string
	)
	switch {
	case source != "":
		label, path = "--source", source
	case os.Getenv("TEAMS_MESSAGES_FILE") != "":
		label, path = "$TEAMS_MESSAGES_FILE", os.Getenv("TEAMS_MESSAGES_FILE")
	default:
		label, path = "default", filepath.Join(DataDirDefault(), "messages.jsonl")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(abs); err != nil || fi.IsDir() {
		return "", fmt.Errorf(
			"data source not found or unreadable (%s): %s\n"+
				"Resolution order: --source <path> → $TEAMS_MESSAGES_FILE → %s\n"+
				"Capture some messages first with: sessh teams capture",
			label, abs, filepath.Join(DataDirDefault(), "messages.jsonl"))
	}
	return abs, nil
}

// ConversationsPathFor locates the sidecar beside a messages file.
func ConversationsPathFor(messagesPath string) string {
	return filepath.Join(filepath.Dir(messagesPath), "conversations.jsonl")
}

// ScanResult reports what a streaming read saw.
type ScanResult struct {
	Total    int // current well-formed records read (last record wins by id)
	Physical int // all well-formed JSONL records, including superseded upserts
	Bad      int // malformed lines skipped
}

// StreamMessages reads the latest complete record per message ID. onBad is
// called with the 1-based line number of each malformed record.
func StreamMessages(path string, onRecord func(*Message), onBad func(int)) (ScanResult, error) {
	snapshot, err := snapshotMessages(path, onBad)
	if err != nil {
		return snapshot.stats, err
	}
	err = snapshot.scan(func(m *Message) {
		recoverStoredAttribution(m)
		snapshot.stats.Total++
		onRecord(m)
	})
	return snapshot.stats, err
}

// Queries and attribution backfills use the same fixed byte boundary and
// last-record-wins index. Concurrent appends cannot change this snapshot.
type messageSnapshot struct {
	path  string
	limit int64
	last  map[string]int
	stats ScanResult
}

func snapshotMessages(path string, onBad func(int)) (*messageSnapshot, error) {
	s := &messageSnapshot{path: path, last: map[string]int{}}
	f, err := os.Open(path)
	if err != nil {
		return s, fmt.Errorf("cannot open %s: %w", path, err)
	}
	s.limit, err = completeJSONLSize(f)
	_ = f.Close()
	if err != nil {
		return s, fmt.Errorf("reading %s: %w", path, err)
	}
	err = scanMessagesTo(path, s.limit, func(m *Message, lineNo int) {
		s.stats.Physical++
		if m.ID != "" {
			s.last[m.ID] = lineNo
		}
	}, func(lineNo int) {
		s.stats.Bad++
		if onBad != nil {
			onBad(lineNo)
		}
	})
	return s, err
}

func (s *messageSnapshot) scan(onRecord func(*Message)) error {
	return scanMessagesTo(s.path, s.limit, func(m *Message, lineNo int) {
		if m.ID == "" || s.last[m.ID] == lineNo {
			onRecord(m)
		}
	}, nil)
}

func completeJSONLSize(f *os.File) (int64, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	size := st.Size()
	if size == 0 {
		return 0, nil
	}
	const chunk = int64(64 * 1024)
	buf := make([]byte, chunk)
	for end := size; end > 0; {
		start := end - chunk
		if start < 0 {
			start = 0
		}
		n, err := f.ReadAt(buf[:end-start], start)
		if err != nil && err != io.EOF {
			return 0, err
		}
		if i := strings.LastIndexByte(string(buf[:n]), '\n'); i >= 0 {
			return start + int64(i) + 1, nil
		}
		end = start
	}
	return 0, nil // no complete JSONL record yet
}

func scanMessagesTo(path string, limit int64, onRecord func(*Message, int), onBad func(int)) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("cannot open %s: %w", path, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, limit))
	sc.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			if onBad != nil {
				onBad(lineNo)
			}
			continue
		}
		onRecord(&m, lineNo)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("error reading %s: %w", path, err)
	}
	return nil
}

// Collect streams a file and keeps the records a predicate accepts.
func Collect(path string, keep func(*Message) bool, onBad func(int)) ([]*Message, ScanResult, error) {
	var rows []*Message
	res, err := StreamMessages(path, func(m *Message) {
		if keep == nil || keep(m) {
			rows = append(rows, m)
		}
	}, onBad)
	return rows, res, err
}

// ---------------------------------------------------------------------------
// Conversation directory
// ---------------------------------------------------------------------------

// Conversation is one entry of the sidecar: what turns a conversationId GUID
// into a human-readable chat, meeting, team or channel name.
type Conversation struct {
	ID                 string   `json:"id"`
	Title              string   `json:"title,omitempty"`
	DisplayName        string   `json:"displayName,omitempty"`
	ShortTitle         string   `json:"shortTitle,omitempty"`
	LongTitle          string   `json:"longTitle,omitempty"`
	Type               string   `json:"type,omitempty"`
	Kind               string   `json:"kind,omitempty"`
	ChannelName        string   `json:"channelName,omitempty"`
	TeamID             string   `json:"teamId,omitempty"`
	GroupID            string   `json:"groupId,omitempty"`
	TeamName           string   `json:"teamName,omitempty"`
	LastMessageTimeUTC any      `json:"lastMessageTimeUtc,omitempty"`
	UpdatedAt          string   `json:"updatedAt,omitempty"`
	Members            []string `json:"-"`
	MemberMRIs         []string `json:"-"`
}

// Name is the best label available for a conversation.
func (c *Conversation) Name() string {
	if c != nil && c.KindOrType() == "channel" && c.ChannelName != "" {
		if c.TeamName != "" {
			return c.TeamName + " / " + c.ChannelName
		}
		if c.TeamID != "" {
			return compactConversationID(c.TeamID) + " / " + c.ChannelName
		}
		return c.ChannelName + " [team unresolved: " + compactConversationID(c.ID) + "]"
	}
	return cmp.Or(c.DisplayName, c.Title, c.ShortTitle, c.LongTitle)
}

// KindOrType is the normalized conversation kind, falling back to the raw type.
func (c *Conversation) KindOrType() string {
	return cmp.Or(c.Kind, c.Type)
}

// Directory is the loaded sidecar. It degrades to empty (id-only mode) when the
// file is absent or unreadable — a missing sidecar must never fail a query.
type Directory struct {
	Map        map[string]*Conversation
	NamesByMRI map[string]string
	Present    bool
	Path       string
}

// LoadConversations reads the sidecar next to a messages file.
func LoadConversations(messagesPath string) *Directory {
	p := ConversationsPathFor(messagesPath)
	dir := &Directory{Map: map[string]*Conversation{}, NamesByMRI: map[string]string{}, Path: p}
	b, err := os.ReadFile(p)
	if err != nil {
		return dir
	}
	dir.Present = true
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		// Producers vary in how they spell titles and members, so decode
		// defensively rather than insisting on one shape.
		var raw struct {
			Conversation
			ChatTitle *struct {
				Short   string `json:"short"`
				Long    string `json:"long"`
				Members []struct {
					DisplayName string `json:"displayName"`
					Name        string `json:"name"`
					MRI         string `json:"mri"`
				} `json:"members"`
			} `json:"chatTitle"`
			RawMembers []json.RawMessage `json:"members"`
		}
		if json.Unmarshal([]byte(t), &raw) != nil || raw.ID == "" {
			continue
		}
		c := raw.Conversation
		if raw.ChatTitle != nil {
			if c.ShortTitle == "" {
				c.ShortTitle = raw.ChatTitle.Short
			}
			if c.LongTitle == "" {
				c.LongTitle = raw.ChatTitle.Long
			}
			if c.Title == "" {
				c.Title = cmp.Or(raw.ChatTitle.Short, raw.ChatTitle.Long)
			}
		}
		if c.Title == "" {
			c.Title = cmp.Or(c.ShortTitle, c.LongTitle)
		}
		if c.DisplayName == "" {
			c.DisplayName = c.Title
		}
		c.Members, c.MemberMRIs = decodeMembers(raw.RawMembers)
		if len(c.Members) == 0 && raw.ChatTitle != nil {
			for _, m := range raw.ChatTitle.Members {
				mri := canonicalAuthorMRI(m.MRI)
				if n := cmp.Or(cleanDisplayName(m.DisplayName), cleanDisplayName(m.Name), mri); n != "" {
					c.Members = append(c.Members, n)
					// Keep the slices index-aligned even for legacy members that
					// carry a display name but no stable MRI.
					c.MemberMRIs = append(c.MemberMRIs, mri)
				}
			}
		}
		for i, mri := range c.MemberMRIs {
			if mri == "" || i >= len(c.Members) {
				continue
			}
			if name := cleanDisplayName(c.Members[i]); name != "" {
				dir.NamesByMRI[strings.ToLower(mri)] = name
			}
		}
		copy := c
		dir.Map[c.ID] = &copy
	}
	return dir
}

func decodeMembers(raws []json.RawMessage) ([]string, []string) {
	var names, mris []string
	for _, raw := range raws {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if s != "" {
				names = append(names, s)
				mris = append(mris, canonicalAuthorMRI(s))
			}
			continue
		}
		var m struct {
			DisplayName string `json:"displayName"`
			Name        string `json:"name"`
			MRI         string `json:"mri"`
		}
		if json.Unmarshal(raw, &m) == nil {
			mri := canonicalAuthorMRI(m.MRI)
			if n := cmp.Or(cleanDisplayName(m.DisplayName), cleanDisplayName(m.Name), mri); n != "" {
				names = append(names, n)
				mris = append(mris, mri)
			}
		}
	}
	return names, mris
}

// Enrich attaches human-readable conversation metadata to a message without
// mutating what was captured.
func (d *Directory) Enrich(m *Message) *Message {
	if d == nil || m == nil {
		return m
	}
	if m.Sender == "" && m.AuthorMRI != "" {
		if name := d.NamesByMRI[strings.ToLower(m.AuthorMRI)]; name != "" {
			m.Sender = name
			m.AuthorStatus = authorResolved
		}
	}
	meta := d.Map[m.ConversationID]
	if meta == nil {
		return m
	}
	m.ConversationTitle = meta.Name()
	m.ConversationType = meta.KindOrType()
	m.ChannelName = meta.ChannelName
	m.TeamID = meta.TeamID
	m.TeamName = meta.TeamName
	return m
}

func compactConversationID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 18 {
		return id
	}
	return id[:15] + "…"
}
