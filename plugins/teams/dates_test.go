package teams

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// datesNow is the fixed clock every ParseWhen case is measured against.
var datesNow = time.Date(2023, 11, 15, 12, 0, 0, 0, time.UTC)

// datesLocalMS parses a wall-clock literal in the test process's zone, which is what
// ParseWhen does for zone-less input. Deriving expectations the same way keeps
// these tests correct wherever they run.
func datesLocalMS(t *testing.T, layout, value string) int64 {
	t.Helper()
	ts, err := time.ParseInLocation(layout, value, time.Local)
	if err != nil {
		t.Fatalf("bad fixture %q: %v", value, err)
	}
	return ts.UnixMilli()
}

func datesStartOfDayMS(ts time.Time) int64 {
	y, m, d := ts.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, ts.Location()).UnixMilli()
}

func TestParseWhen(t *testing.T) {
	const (
		minute = int64(60_000)
		hour   = 60 * minute
		day    = 24 * hour
		week   = 7 * day
	)

	tests := []struct {
		name string
		in   string
		want func(t *testing.T) int64
	}{
		// --- keywords -------------------------------------------------------
		{"now", "now", func(*testing.T) int64 { return datesNow.UnixMilli() }},
		{"now is case-insensitive", "NOW", func(*testing.T) int64 { return datesNow.UnixMilli() }},
		{"today is the start of the LOCAL day", "today", func(*testing.T) int64 { return datesStartOfDayMS(datesNow) }},
		{"yesterday is one day before that", "yesterday", func(*testing.T) int64 { return datesStartOfDayMS(datesNow) - day }},
		{"surrounding whitespace is trimmed", "  today  ", func(*testing.T) int64 { return datesStartOfDayMS(datesNow) }},

		// --- relative durations --------------------------------------------
		{"30m", "30m", func(*testing.T) int64 { return datesNow.UnixMilli() - 30*minute }},
		{"min is a synonym for m", "30min", func(*testing.T) int64 { return datesNow.UnixMilli() - 30*minute }},
		{"2h", "2h", func(*testing.T) int64 { return datesNow.UnixMilli() - 2*hour }},
		{"hr is a synonym for h", "2hr", func(*testing.T) int64 { return datesNow.UnixMilli() - 2*hour }},
		{"the plural spelling works", "2 hours", func(*testing.T) int64 { return datesNow.UnixMilli() - 2*hour }},
		{"7d", "7d", func(*testing.T) int64 { return datesNow.UnixMilli() - 7*day }},
		{"days spelled out", "7 days", func(*testing.T) int64 { return datesNow.UnixMilli() - 7*day }},
		{"2w", "2w", func(*testing.T) int64 { return datesNow.UnixMilli() - 2*week }},
		{"weeks spelled out", "2 weeks", func(*testing.T) int64 { return datesNow.UnixMilli() - 2*week }},
		{"the unit is case-insensitive", "2H", func(*testing.T) int64 { return datesNow.UnixMilli() - 2*hour }},
		{"zero of a unit is now", "0d", func(*testing.T) int64 { return datesNow.UnixMilli() }},

		// --- epoch ------------------------------------------------------------
		{"epoch seconds are scaled up", "1700000000", func(*testing.T) int64 { return 1700000000000 }},
		{"epoch milliseconds are taken as-is", "1700000000000", func(*testing.T) int64 { return 1700000000000 }},
		{"the seconds/ms boundary is 1e12", "999999999999", func(*testing.T) int64 { return 999999999999000 }},
		{"exactly 1e12 is already milliseconds", "1000000000000", func(*testing.T) int64 { return 1000000000000 }},
		{
			// Documented rule: a bare number is epoch, never an ISO year.
			name: "a bare four-digit number is epoch seconds, not a year",
			in:   "2026",
			want: func(*testing.T) int64 { return 2026000 },
		},
		{"zero is the epoch itself", "0", func(*testing.T) int64 { return 0 }},

		// --- ISO-8601 ---------------------------------------------------------
		{
			name: "a date is midnight local",
			in:   "2023-11-14",
			want: func(t *testing.T) int64 { return datesLocalMS(t, "2006-01-02", "2023-11-14") },
		},
		{
			name: "a zoned datetime is absolute",
			in:   "2023-11-14T14:00:00Z",
			want: func(*testing.T) int64 { return time.Date(2023, 11, 14, 14, 0, 0, 0, time.UTC).UnixMilli() },
		},
		{
			name: "an offset datetime is absolute",
			in:   "2023-11-14T14:00:00+02:00",
			want: func(*testing.T) int64 { return time.Date(2023, 11, 14, 12, 0, 0, 0, time.UTC).UnixMilli() },
		},
		{
			name: "fractional seconds are accepted",
			in:   "2023-11-14T14:00:00.500Z",
			want: func(*testing.T) int64 { return time.Date(2023, 11, 14, 14, 0, 0, 5e8, time.UTC).UnixMilli() },
		},
		{
			name: "a zone-less datetime is local",
			in:   "2023-11-14T14:00:00",
			want: func(t *testing.T) int64 { return datesLocalMS(t, "2006-01-02T15:04:05", "2023-11-14T14:00:00") },
		},
		{
			name: "a space-separated datetime is local",
			in:   "2023-11-14 14:00:00",
			want: func(t *testing.T) int64 { return datesLocalMS(t, "2006-01-02 15:04:05", "2023-11-14 14:00:00") },
		},
		{
			name: "minutes-only precision is accepted",
			in:   "2023-11-14 14:00",
			want: func(t *testing.T) int64 { return datesLocalMS(t, "2006-01-02 15:04", "2023-11-14 14:00") },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseWhen("--since", tt.in, datesNow)
			if err != nil {
				t.Fatalf("ParseWhen(%q) error = %v, want nil", tt.in, err)
			}
			if want := tt.want(t); got != want {
				t.Errorf("ParseWhen(%q) = %d, want %d (delta %d ms)", tt.in, got, want, got-want)
			}
		})
	}
}

func TestParseWhenFailures(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"an empty value", ""},
		{"whitespace only", "   "},
		{"free text", "not-a-date"},
		{"a keyword we do not support", "tomorrow"},
		{"a keyword we do not support either", "last week"},
		{"an unknown unit", "5x"},
		{"a unit with no quantity", "d"},
		{"a negative relative value", "-2h"},
		{"a fractional relative value", "1.5h"},
		{"an impossible date", "2023-13-45"},
		{"a date with slashes", "14/11/2023"},
		{"an epoch with a suffix", "1700000000ms"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseWhen("--until", tt.in, datesNow)
			if err == nil {
				t.Fatalf("ParseWhen(%q) = %d, want an error", tt.in, got)
			}
			var perr *DateParseError
			if !errors.As(err, &perr) {
				t.Fatalf("ParseWhen(%q) error type = %T, want *DateParseError", tt.in, err)
			}
			if perr.Flag != "--until" {
				t.Errorf("Flag = %q, want --until", perr.Flag)
			}
			if perr.Input != tt.in {
				t.Errorf("Input = %q, want the original %q", perr.Input, tt.in)
			}
		})
	}

	t.Run("the message names the flag and lists every accepted format", func(t *testing.T) {
		_, err := ParseWhen("--since", "nope", datesNow)
		if err == nil {
			t.Fatal("want an error")
		}
		msg := err.Error()
		if !strings.Contains(msg, "--since") {
			t.Errorf("message %q does not name the flag", msg)
		}
		if !strings.Contains(msg, `"nope"`) {
			t.Errorf("message %q does not quote the input", msg)
		}
		if len(AcceptedFormats) < 4 {
			t.Fatalf("AcceptedFormats has %d entries, want at least 4", len(AcceptedFormats))
		}
		for _, f := range AcceptedFormats {
			if !strings.Contains(msg, f) {
				t.Errorf("message is missing the accepted format %q", f)
			}
		}
	})
}

func TestMessageTimeMS(t *testing.T) {
	tests := []struct {
		name string
		msg  *Message
		want int64
		ok   bool
	}{
		{
			name: "composeTime epoch-ms wins over capturedAt",
			msg:  &Message{ComposeTime: "1700000100000", CapturedAt: "2020-01-01T00:00:00Z"},
			want: 1700000100000, ok: true,
		},
		{
			name: "composeTime epoch-seconds is scaled up",
			msg:  &Message{ComposeTime: "1700000100"},
			want: 1700000100000, ok: true,
		},
		{
			name: "composeTime may also arrive as ISO",
			msg:  &Message{ComposeTime: "2023-11-14T22:15:00.000Z"},
			want: 1700000100000, ok: true,
		},
		{
			name: "whitespace around composeTime is tolerated",
			msg:  &Message{ComposeTime: "  1700000100000  "},
			want: 1700000100000, ok: true,
		},
		{
			name: "capturedAt is the fallback when composeTime is missing",
			msg:  &Message{CapturedAt: "2023-11-14T22:15:00.000Z"},
			want: 1700000100000, ok: true,
		},
		{
			name: "an unparseable composeTime still falls back to capturedAt",
			msg:  &Message{ComposeTime: "garbage", CapturedAt: "2023-11-14T22:15:00.000Z"},
			want: 1700000100000, ok: true,
		},
		{
			name: "an empty composeTime falls back to capturedAt",
			msg:  &Message{ComposeTime: "", CapturedAt: "2023-11-14T22:15:00.000Z"},
			want: 1700000100000, ok: true,
		},
		{
			name: "capturedAt without fractional seconds still parses",
			msg:  &Message{CapturedAt: "2023-11-14T22:15:00Z"},
			want: 1700000100000, ok: true,
		},
		{name: "neither field means the time is unknown", msg: &Message{}, ok: false},
		{name: "an unparseable composeTime alone is unknown", msg: &Message{ComposeTime: "garbage"}, ok: false},
		{
			name: "an unparseable capturedAt is unknown rather than zero",
			msg:  &Message{CapturedAt: "not a time"}, ok: false,
		},
		{name: "a nil message is unknown, not a panic", msg: nil, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := MessageTimeMS(tt.msg)
			if ok != tt.ok {
				t.Fatalf("MessageTimeMS() ok = %v, want %v", ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("MessageTimeMS() = %d, want %d", got, tt.want)
			}
		})
	}

	t.Run("composeTime survives arriving as a JSON number", func(t *testing.T) {
		var m Message
		if err := json.Unmarshal([]byte(`{"composeTime":1700000100000}`), &m); err != nil {
			t.Fatal(err)
		}
		got, ok := MessageTimeMS(&m)
		if !ok || got != 1700000100000 {
			t.Errorf("MessageTimeMS() = (%d, %v), want (1700000100000, true)", got, ok)
		}
	})
}

func TestToISO(t *testing.T) {
	tests := []struct {
		name string
		ms   int64
		ok   bool
		want string
	}{
		{"a known time renders as UTC with milliseconds", 1700000100000, true, "2023-11-14T22:15:00.000Z"},
		{"milliseconds are not truncated", 1700000100123, true, "2023-11-14T22:15:00.123Z"},
		{"the epoch itself", 0, true, "1970-01-01T00:00:00.000Z"},
		{"an unknown time renders as an empty string, not a fake date", 1700000100000, false, ""},
		{"a zero value with ok=false is still empty", 0, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ToISO(tt.ms, tt.ok); got != tt.want {
				t.Errorf("ToISO(%d, %v) = %q, want %q", tt.ms, tt.ok, got, tt.want)
			}
		})
	}
}

func TestToLocal(t *testing.T) {
	t.Run("an unknown time is labelled rather than blank", func(t *testing.T) {
		if got := ToLocal(0, false); got != "(no time)" {
			t.Errorf("ToLocal(_, false) = %q, want %q", got, "(no time)")
		}
	})
	t.Run("a known time renders in the local zone", func(t *testing.T) {
		const ms = int64(1700000100000)
		want := time.UnixMilli(ms).Local().Format("2006-01-02 15:04:05")
		if got := ToLocal(ms, true); got != want {
			t.Errorf("ToLocal() = %q, want %q", got, want)
		}
	})
	t.Run("the rendering has no zone suffix, so it reads as wall-clock", func(t *testing.T) {
		got := ToLocal(1700000100000, true)
		if len(got) != len("2006-01-02 15:04:05") {
			t.Errorf("ToLocal() = %q, want a bare wall-clock stamp", got)
		}
	})
}

func TestDayKey(t *testing.T) {
	t.Run("an unknown time gets its own bucket", func(t *testing.T) {
		if got := DayKey(0, false); got != "(no time)" {
			t.Errorf("DayKey(_, false) = %q, want %q", got, "(no time)")
		}
	})
	t.Run("buckets follow the LOCAL day, matching --since today", func(t *testing.T) {
		const ms = int64(1700000100000)
		want := time.UnixMilli(ms).Local().Format("2006-01-02")
		if got := DayKey(ms, true); got != want {
			t.Errorf("DayKey() = %q, want %q", got, want)
		}
	})
	t.Run("a timestamp and the start of its local day share a bucket", func(t *testing.T) {
		ms := time.Date(2023, 11, 14, 22, 15, 0, 0, time.Local).UnixMilli()
		startMS, err := ParseWhen("--since", "today", time.UnixMilli(ms))
		if err != nil {
			t.Fatal(err)
		}
		if DayKey(ms, true) != DayKey(startMS, true) {
			t.Errorf("DayKey(%d) = %q but DayKey(start of that day) = %q",
				ms, DayKey(ms, true), DayKey(startMS, true))
		}
	})
}

func TestMetadataTimeMS(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want int64
		ok   bool
	}{
		{name: "a missing value is unknown", in: nil, ok: false},
		{name: "an epoch-seconds number is scaled up", in: float64(1700000000), want: 1700000000000, ok: true},
		{name: "an epoch-ms number is taken as-is", in: float64(1700000100000), want: 1700000100000, ok: true},
		{name: "the -1 sentinel means unknown", in: float64(-1), ok: false},
		{name: "zero means unknown", in: float64(0), ok: false},
		{name: "an epoch-ms string", in: "1700000100000", want: 1700000100000, ok: true},
		{name: "an epoch-seconds string", in: "1700000000", want: 1700000000000, ok: true},
		{name: "the -1 sentinel as a string means unknown", in: "-1", ok: false},
		{name: "the zero string means unknown", in: "0", ok: false},
		{name: "an empty string means unknown", in: "", ok: false},
		{name: "a whitespace-only string means unknown", in: "   ", ok: false},
		{name: "an ISO string is parsed", in: "2023-11-14T22:15:00.000Z", want: 1700000100000, ok: true},
		{name: "an ISO string without fractions is parsed", in: "2023-11-14T22:15:00Z", want: 1700000100000, ok: true},
		{name: "a date-only string is parsed", in: "2023-11-14", want: time.Date(2023, 11, 14, 0, 0, 0, 0, time.UTC).UnixMilli(), ok: true},
		{name: "surrounding whitespace is trimmed", in: "  1700000100000 ", want: 1700000100000, ok: true},
		{name: "free text means unknown", in: "never", ok: false},
		{name: "a boolean means unknown", in: true, ok: false},
		{name: "a nested object means unknown", in: map[string]any{"x": 1}, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := metadataTimeMS(tt.in)
			if ok != tt.ok {
				t.Fatalf("metadataTimeMS(%v) ok = %v, want %v", tt.in, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("metadataTimeMS(%v) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}

	t.Run("values decoded from the sidecar JSON behave the same", func(t *testing.T) {
		cases := []struct {
			line string
			want int64
			ok   bool
		}{
			{`{"lastMessageTimeUtc":1700000100000}`, 1700000100000, true},
			{`{"lastMessageTimeUtc":"1700000100000"}`, 1700000100000, true},
			{`{"lastMessageTimeUtc":-1}`, 0, false},
			{`{"lastMessageTimeUtc":null}`, 0, false},
			{`{}`, 0, false},
		}
		for _, c := range cases {
			var conv Conversation
			if err := json.Unmarshal([]byte(c.line), &conv); err != nil {
				t.Fatalf("%s: %v", c.line, err)
			}
			got, ok := metadataTimeMS(conv.LastMessageTimeUTC)
			if ok != c.ok || (ok && got != c.want) {
				t.Errorf("%s -> (%d, %v), want (%d, %v)", c.line, got, ok, c.want, c.ok)
			}
		}
	})
}
