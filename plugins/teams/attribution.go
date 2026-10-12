package teams

import (
	"encoding/json"
	"encoding/xml"
	"regexp"
	"strings"
)

// Attribution is persisted separately from the display name. Teams display
// names are mutable and need not be unique; an MRI is the durable identity.
// Conversation MRIs (19:...) are deliberately not author identities.
const (
	authorResolved        = "resolved"
	authorUnresolved      = "unresolved"
	authorSystem          = "system"
	authorDisplayNameOnly = "display-name-only"
)

var (
	authorMRIRe        = regexp.MustCompile(`(?i)^(8:[a-z][a-z0-9._-]*:|28:).+`)
	conversationLinkRe = regexp.MustCompile(`(?i)(?:^|/)conversation/([^;/\s?#]+)`)
	rawDisplayNameRe   = regexp.MustCompile(`(?i)^(?:worker/|(?:8:)?(?:orgid|live):|undefined$|null$)`)
)

// identityResolver is intentionally one-way. Two people may share a display
// name, while one MRI may legitimately acquire a newer display name after a
// rename. The latest observation wins presentation; grouping still uses MRI.
type identityResolver struct {
	names map[string]string
}

func newIdentityResolver() *identityResolver {
	return &identityResolver{names: map[string]string{}}
}

func (r *identityResolver) observe(mri, name string) {
	if r == nil {
		return
	}
	mri = canonicalAuthorMRI(mri)
	name = cleanDisplayName(name)
	if mri != "" && name != "" {
		r.names[strings.ToLower(mri)] = name
	}
}

func (r *identityResolver) resolve(mri string) string {
	if r == nil {
		return ""
	}
	return r.names[strings.ToLower(canonicalAuthorMRI(mri))]
}

// observeRaw learns only from typed identity/displayName pairs in Teams'
// application/user+xml call-participant payload. It does not scrape rendered
// body text, where an MRI next to a name could be quoted or otherwise unrelated
// to the author.
func (r *identityResolver) observeRaw(raw map[string]any) {
	if r == nil || raw == nil {
		return
	}
	contentType := strings.ToLower(jsonString(pickField(raw, "contentType", "ContentType", "contenttype")))
	content := jsonString(pickField(raw, "content", "Content"))
	if !strings.Contains(contentType, "application/user+xml") ||
		!strings.Contains(strings.ToLower(content), "<part") {
		return
	}
	dec := xml.NewDecoder(strings.NewReader(content))
	dec.Strict = false // Teams emits XML fragments with more than one root node.
	var partMRI string
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch strings.ToLower(t.Name.Local) {
			case "part":
				partMRI = ""
				for _, attr := range t.Attr {
					if strings.EqualFold(attr.Name.Local, "identity") {
						partMRI = canonicalAuthorMRI(attr.Value)
						break
					}
				}
			case "name":
				if partMRI == "" {
					var value string
					if dec.DecodeElement(&value, &t) == nil {
						partMRI = canonicalAuthorMRI(value)
					}
				}
			case "displayname":
				if partMRI != "" {
					var name string
					if dec.DecodeElement(&name, &t) == nil {
						r.observe(partMRI, name)
					}
				}
			}
		case xml.EndElement:
			if strings.EqualFold(t.Name.Local, "part") {
				partMRI = ""
			}
		}
	}
}

// canonicalAuthorMRI accepts person and application/bot MRIs. A bare
// orgid:/live: value is a malformed-but-recoverable person MRI seen in Teams'
// imDisplayName field. worker/ is transport decoration, never part of identity.
func canonicalAuthorMRI(s string) string {
	s = strings.TrimSpace(s)
	for strings.HasPrefix(strings.ToLower(s), "worker/") {
		s = strings.TrimSpace(s[len("worker/"):])
	}
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "orgid:") || strings.HasPrefix(lower, "live:") {
		s = "8:" + s
		lower = strings.ToLower(s)
	}
	if !authorMRIRe.MatchString(s) || strings.ContainsAny(s, " \t\r\n") {
		return ""
	}
	// Prefixes are protocol tokens and are case-insensitive. Preserve the tail
	// because 8:live identities are not guaranteed to be UUIDs.
	if strings.HasPrefix(lower, "8:") {
		parts := strings.SplitN(s, ":", 3)
		if len(parts) != 3 || strings.TrimSpace(parts[2]) == "" {
			return ""
		}
		return "8:" + strings.ToLower(parts[1]) + ":" + parts[2]
	}
	return "28:" + strings.TrimPrefix(strings.TrimPrefix(s, "28:"), "28:")
}

func cleanDisplayName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || rawDisplayNameRe.MatchString(s) || canonicalAuthorMRI(s) != "" {
		return ""
	}
	if (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) ||
		(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) {
		return ""
	}
	return s
}

func nestedMap(o map[string]any, names ...string) map[string]any {
	for _, name := range names {
		if m, ok := o[name].(map[string]any); ok {
			return m
		}
	}
	return nil
}

// authorMRIOf resolves only author-shaped MRIs. In particular, creator on a
// ThreadActivity record is commonly the 19:... conversation MRI and is ignored.
func authorMRIOf(raw map[string]any) string {
	if raw == nil {
		return ""
	}
	from := pickField(raw, "from", "From")
	if fm, ok := from.(map[string]any); ok {
		for _, child := range []map[string]any{
			nestedMap(fm, "user", "User"),
			nestedMap(fm, "application", "Application", "app", "App"),
		} {
			if child == nil {
				continue
			}
			if mri := canonicalAuthorMRI(jsonString(pickField(child, "id", "Id", "mri", "MRI"))); mri != "" {
				return mri
			}
		}
		if mri := canonicalAuthorMRI(jsonString(pickField(fm, "id", "Id", "mri", "MRI"))); mri != "" {
			return mri
		}
	}
	for _, candidate := range []any{
		from,
		pickField(raw, "originalSender", "OriginalSender"),
		pickField(raw, "creator", "Creator"),
		pickField(raw, "imDisplayName", "ImDisplayName"),
		pickField(raw, "sender", "Sender"),
	} {
		if mri := canonicalAuthorMRI(jsonString(candidate)); mri != "" {
			return mri
		}
	}
	return ""
}

func senderOf(raw map[string]any) string {
	if raw == nil {
		return ""
	}
	switch from := pickField(raw, "from", "From").(type) {
	case string:
		if name := cleanDisplayName(from); name != "" {
			return name
		}
	case map[string]any:
		for _, child := range []map[string]any{
			nestedMap(from, "user", "User"),
			nestedMap(from, "application", "Application", "app", "App"),
		} {
			if child == nil {
				continue
			}
			if name := cleanDisplayName(jsonString(pickField(child,
				"displayName", "DisplayName", "name", "Name"))); name != "" {
				return name
			}
		}
		if name := cleanDisplayName(jsonString(pickField(from,
			"displayName", "DisplayName", "imDisplayName", "ImDisplayName", "name", "Name"))); name != "" {
			return name
		}
	}
	return cleanDisplayName(jsonString(pickField(raw,
		"imDisplayName", "ImDisplayName", "sender", "Sender")))
}

func conversationIDOf(raw map[string]any) (string, string) {
	if raw == nil {
		return "", ""
	}
	if id := validConversationID(jsonString(pickField(raw,
		"conversationId", "ConversationId", "conversationid",
		"chatId", "ChatId", "channelId", "ChannelId"))); id != "" {
		return id, "direct"
	}
	if id := validConversationID(jsonString(pickField(raw,
		"originalThreadId", "OriginalThreadId"))); id != "" {
		return id, "originalThreadId"
	}
	link := strings.TrimSpace(jsonString(pickField(raw, "conversationLink", "ConversationLink")))
	if match := conversationLinkRe.FindStringSubmatch(link); len(match) == 2 {
		if id := validConversationID(match[1]); id != "" {
			return id, "conversationLink"
		}
	}
	return "", ""
}

func validConversationID(s string) string {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "", "undefined", "null", "(none)":
		return ""
	}
	return s
}

// normalizeMessageAttribution is shared by capture, backward-compatible reads,
// and the append-only backfill. It never invents a display name or treats a
// conversation MRI as a person.
func normalizeMessageAttribution(m *Message, raw map[string]any, resolver *identityResolver) {
	if m == nil {
		return
	}
	if m.ConversationID = validConversationID(m.ConversationID); m.ConversationID == "" {
		m.ConversationID, _ = conversationIDOf(raw)
	}
	m.AuthorMRI = canonicalAuthorMRI(m.AuthorMRI)
	if m.AuthorMRI == "" {
		m.AuthorMRI = authorMRIOf(raw)
	}
	m.Sender = cleanDisplayName(m.Sender)
	if m.Sender == "" {
		m.Sender = senderOf(raw)
	}
	resolver.observeRaw(raw)
	if m.Sender == "" && m.AuthorMRI != "" {
		m.Sender = resolver.resolve(m.AuthorMRI)
	}
	switch {
	case m.AuthorMRI != "" && m.Sender != "":
		m.AuthorStatus = authorResolved
		resolver.observe(m.AuthorMRI, m.Sender)
	case m.AuthorMRI != "":
		m.AuthorStatus = authorUnresolved
	case isSystemMessage(m):
		m.AuthorStatus = authorSystem
	case m.Sender != "":
		m.AuthorStatus = authorDisplayNameOnly
	default:
		m.AuthorStatus = authorUnresolved
	}
}

func recoverStoredAttribution(m *Message) {
	if m == nil {
		return
	}
	var raw map[string]any
	if len(m.Raw) > 0 {
		_ = json.Unmarshal(m.Raw, &raw)
	}
	normalizeMessageAttribution(m, raw, nil)
}

func authorLabel(m *Message) string {
	if m == nil {
		return "(unresolved author)"
	}
	if name := cleanDisplayName(m.Sender); name != "" {
		return name
	}
	switch {
	case m.AuthorStatus == authorSystem:
		return "(system)"
	case m.AuthorMRI != "":
		return "(unresolved user: " + m.AuthorMRI + ")"
	default:
		return "(unresolved author)"
	}
}

func authorGroupKey(m *Message) string {
	if m != nil && m.AuthorMRI != "" {
		return "mri:" + strings.ToLower(m.AuthorMRI)
	}
	if m == nil {
		return "status:unresolved"
	}
	return "status:" + m.AuthorStatus + ":" + strings.ToLower(authorLabel(m))
}
