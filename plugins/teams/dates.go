package teams

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Date handling is the trickiest pure logic here. It must accept four input
// shapes and, crucially, fail loudly with the accepted-formats list rather than
// silently misinterpret an agent's input.

// AcceptedFormats is surfaced on a parse error.
var AcceptedFormats = []string{
	"ISO-8601:     2026-07-06  |  2026-07-06T14:00:00Z",
	"relative:     30m, 2h, 7d, 2w  (ago)",
	"keywords:     today, yesterday, now",
	"epoch:        seconds or milliseconds (auto-detected)",
}

// DateParseError names the input that could not be understood.
type DateParseError struct {
	Flag  string
	Input string
}

func (e *DateParseError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: unrecognized date/time %q. Accepted formats:", e.Flag, e.Input)
	for _, f := range AcceptedFormats {
		b.WriteString("\n  " + f)
	}
	return b.String()
}

var relativeRe = regexp.MustCompile(`^(\d+)\s*(m|min|h|hr|hour|hours|d|day|days|w|week|weeks)$`)

var unitMS = map[string]int64{
	"m": 60_000, "min": 60_000,
	"h": 3_600_000, "hr": 3_600_000, "hour": 3_600_000, "hours": 3_600_000,
	"d": 86_400_000, "day": 86_400_000, "days": 86_400_000,
	"w": 604_800_000, "week": 604_800_000, "weeks": 604_800_000,
}

var allDigits = regexp.MustCompile(`^\d+$`)

// epochToMs distinguishes seconds from milliseconds by magnitude: ~1e12 ms is
// 2001, so anything at or above that is already milliseconds.
func epochToMs(n int64) int64 {
	if n >= 1e12 {
		return n
	}
	return n * 1000
}

func startOfLocalDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// ParseWhen turns a --since/--until value into epoch milliseconds.
func ParseWhen(flag, input string, now time.Time) (int64, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return 0, &DateParseError{Flag: flag, Input: input}
	}
	lower := strings.ToLower(s)

	switch lower {
	case "now":
		return now.UnixMilli(), nil
	case "today":
		return startOfLocalDay(now).UnixMilli(), nil
	case "yesterday":
		return startOfLocalDay(now).UnixMilli() - unitMS["d"], nil
	}

	if m := relativeRe.FindStringSubmatch(lower); m != nil {
		qty, err := strconv.ParseInt(m[1], 10, 64)
		if err == nil {
			return now.UnixMilli() - qty*unitMS[m[2]], nil
		}
	}

	// Epoch, checked before generic parsing so a bare number is never misread
	// as an ISO year.
	if allDigits.MatchString(s) {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return epochToMs(n), nil
		}
	}

	for _, layout := range []string{
		time.RFC3339Nano, time.RFC3339,
		"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02",
	} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.UnixMilli(), nil
		}
	}
	return 0, &DateParseError{Flag: flag, Input: input}
}

// MessageTimeMS derives a message's time: composeTime is epoch milliseconds as
// a string; fall back to capturedAt (ISO) when it is missing or unparseable.
// Returns ok=false when the time is truly unknown.
func MessageTimeMS(m *Message) (int64, bool) {
	if m == nil {
		return 0, false
	}
	if ct := strings.TrimSpace(m.ComposeTime.String()); ct != "" {
		if allDigits.MatchString(ct) {
			if n, err := strconv.ParseInt(ct, 10, 64); err == nil {
				return epochToMs(n), true
			}
		}
		if t, ok := parseAnyTime(ct); ok {
			return t.UnixMilli(), true
		}
	}
	if m.CapturedAt != "" {
		if t, ok := parseAnyTime(m.CapturedAt); ok {
			return t.UnixMilli(), true
		}
	}
	return 0, false
}

func parseAnyTime(s string) (time.Time, bool) {
	for _, layout := range []string{
		time.RFC3339Nano, time.RFC3339,
		"2006-01-02T15:04:05.999Z0700", "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ToISO renders epoch ms as ISO-8601 UTC — the machine contract, so agents
// never parse epoch-ms themselves.
func ToISO(ms int64, ok bool) string {
	if !ok {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}

// ToLocal renders epoch ms for humans.
func ToLocal(ms int64, ok bool) string {
	if !ok {
		return "(no time)"
	}
	return time.UnixMilli(ms).Local().Format("2006-01-02 15:04:05")
}

// DayKey buckets by LOCAL day, so `stats --since today` puts rows in buckets
// that agree with the filter window (no UTC/local split).
func DayKey(ms int64, ok bool) string {
	if !ok {
		return "(no time)"
	}
	return time.UnixMilli(ms).Local().Format("2006-01-02")
}

// metadataTimeMS reads the sidecar's lastMessageTimeUtc, which arrives as an
// epoch number, an epoch string, an ISO string, or the sentinel -1.
func metadataTimeMS(v any) (int64, bool) {
	switch t := v.(type) {
	case nil:
		return 0, false
	case float64:
		if t <= 0 {
			return 0, false
		}
		return epochToMs(int64(t)), true
	case string:
		s := strings.TrimSpace(t)
		if s == "" || s == "-1" {
			return 0, false
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			if n <= 0 {
				return 0, false
			}
			return epochToMs(n), true
		}
		if ts, ok := parseAnyTime(s); ok {
			return ts.UnixMilli(), true
		}
	}
	return 0, false
}
