package teams

import (
	"crypto/sha1"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
)

// dedupe.go defines the content identity of a message, used alongside
// id-dedupe by the capture writer.
//
// Id-dedupe is correct only as long as every copy of a message carries the same
// id, and it does not: the same logical message seen through the IndexedDB
// harvest and through a REST/WS frame can arrive under a server id in one and a
// clientMessageId in the other, so id-dedupe keeps both. Content identity —
// author + conversation + timestamp + body — collapses those.
//
// Two rules prevent missed duplicates and accidental merges:
//
//  1. Timestamps arrive in mixed formats (epoch-ms strings AND ISO-8601).
//     Canonicalize, or the same message from two sources will not match.
//  2. Never content-merge a record with an empty body or an unusable timestamp.
//     Those are system/activity messages that legitimately share
//     (author, conversation) and would false-merge; they fall back to id-only
//     identity by getting no content key at all.

var epochDigits = regexp.MustCompile(`^\d{10,}$`)

// canonicalTS reduces a timestamp to a stable number.
//
// It is deliberately NOT epochToMs: a 10-digit (seconds) value is used as-is.
// This is an identity, not an instant — rescaling numeric timestamps would risk
// colliding a seconds value with a different message's milliseconds value.
func canonicalTS(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if epochDigits.MatchString(s) {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	// Read zone-less strings as UTC so content identity does not depend on the
	// machine's local timezone.
	t, ok := parseAnyTime(s)
	if !ok {
		return 0, false
	}
	return t.UnixMilli(), true
}

// normalizeBody collapses whitespace. Bodies are already HTML-stripped upstream.
func normalizeBody(s string) string { return strings.Join(strings.Fields(s), " ") }

// authorIdentity prefers an MRI, which survives display-name changes and
// collisions, and falls back to the display name. Tolerant of the shape
// variation between sources.
func authorIdentity(m *Message, raw map[string]any) string {
	var persistedMRI, sender string
	if m != nil {
		persistedMRI, sender = m.AuthorMRI, m.Sender
	}
	for _, c := range []string{canonicalAuthorMRI(persistedMRI), authorMRIOf(raw), sender} {
		if t := strings.TrimSpace(c); t != "" {
			return t
		}
	}
	return ""
}

func hashBody(body string) string {
	sum := sha1.Sum([]byte(body))
	return hex.EncodeToString(sum[:])[:16]
}

// contentKeyWithRaw is the write-time key: the caller already holds the decoded
// raw payload, so this avoids re-parsing it. An empty result means "dedupe this
// record on id only".
func contentKeyWithRaw(m *Message, raw map[string]any) string {
	if m == nil {
		return ""
	}
	body := normalizeBody(m.Body)
	if body == "" {
		return ""
	}
	// A ThreadActivity creator is normally its 19:... conversation, not a
	// person. Authorless system rows therefore remain id-only even when their
	// machine body and timestamp are non-empty.
	if isSystemMessage(m) && canonicalAuthorMRI(m.AuthorMRI) == "" && authorMRIOf(raw) == "" {
		return ""
	}
	ts, ok := canonicalTS(m.ComposeTime.String())
	if !ok {
		return ""
	}
	return authorIdentity(m, raw) +
		normalizeBody(m.ConversationID) +
		strconv.FormatInt(ts, 10) +
		hashBody(body)
}

// contentKey is the same key for a record read back off disk, where the raw
// payload is still JSON.
func contentKey(m *Message) string {
	if m == nil {
		return ""
	}
	return contentKeyWithRaw(m, rawObject(m.Raw))
}
