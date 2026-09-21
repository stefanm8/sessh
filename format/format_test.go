package format

import (
	"strings"
	"testing"
	"time"
)

// withTTY forces the TTY branch of Table on (or off) for one test. isTTY is
// decided once at package init from os.Stdout, which under `go test` is always
// a pipe — without this the aligned-column branch would be unreachable.
func withTTY(t *testing.T, on bool) {
	t.Helper()
	prev := isTTY
	isTTY = on
	t.Cleanup(func() { isTTY = prev })
}

// noColor pins color off so table widths are exercised without escape codes
// leaking into the expectations.
func noColor(t *testing.T) {
	t.Helper()
	prev := useColor
	SetColor(false)
	t.Cleanup(func() { useColor = prev })
}

func TestTablePiped(t *testing.T) {
	withTTY(t, false)
	noColor(t)

	tests := []struct {
		name    string
		rows    [][]string
		columns []string
		want    string
	}{
		{
			name: "no rows is the empty string",
			rows: nil,
			want: "",
		},
		{
			name: "empty slice is the empty string",
			rows: [][]string{},
			want: "",
		},
		{
			name:    "rows become tab separated values",
			rows:    [][]string{{"1", "first"}, {"2", "second"}},
			columns: []string{"id", "title"},
			want:    "1\tfirst\n2\tsecond",
		},
		{
			name:    "headers are omitted when piped so cut(1) keeps working",
			rows:    [][]string{{"a", "b"}},
			columns: []string{"col1", "col2"},
			want:    "a\tb",
		},
		{
			name: "cells are not padded when piped",
			rows: [][]string{{"a", "bbbb"}, {"cccc", "d"}},
			want: "a\tbbbb\ncccc\td",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Table(tt.rows, tt.columns); got != tt.want {
				t.Errorf("Table() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTableTTY(t *testing.T) {
	withTTY(t, true)
	noColor(t)

	t.Run("no rows is the empty string even on a TTY", func(t *testing.T) {
		if got := Table(nil, []string{"id"}); got != "" {
			t.Errorf("Table(nil) = %q, want empty", got)
		}
	})

	t.Run("headers are upper-cased and columns are aligned", func(t *testing.T) {
		got := Table([][]string{{"a", "bb"}, {"ccc", "d"}}, []string{"id", "name"})
		want := "ID   NAME\n" +
			"a    bb\n" +
			"ccc  d"
		if got != want {
			t.Errorf("Table() =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("the header widens a column when it is the longest cell", func(t *testing.T) {
		got := Table([][]string{{"a", "b"}}, []string{"identifier", "n"})
		want := "IDENTIFIER  N\n" +
			"a           b"
		if got != want {
			t.Errorf("Table() =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("the last column is never padded", func(t *testing.T) {
		got := Table([][]string{{"a", "long value"}, {"b", "x"}}, nil)
		for _, line := range strings.Split(got, "\n") {
			if line != strings.TrimRight(line, " ") {
				t.Errorf("line %q has trailing padding", line)
			}
		}
	})

	t.Run("without columns no header line is printed", func(t *testing.T) {
		got := Table([][]string{{"a", "b"}}, nil)
		if want := "a  b"; got != want {
			t.Errorf("Table() = %q, want %q", got, want)
		}
	})

	t.Run("padding is measured in visible runes, not bytes", func(t *testing.T) {
		// "日本" is 2 runes / 6 bytes; the colored cell carries 9 extra bytes of
		// ANSI. Both must align to the same width as the 2-rune plain cell.
		got := Table([][]string{{"日本", "x"}, {"\x1b[32mab\x1b[0m", "y"}}, nil)
		want := "日本  x\n\x1b[32mab\x1b[0m  y"
		if got != want {
			t.Errorf("Table() = %q, want %q", got, want)
		}
	})

	t.Run("a short row does not pad past its own last cell", func(t *testing.T) {
		got := Table([][]string{{"aaa", "bbb", "ccc"}, {"d"}}, nil)
		want := "aaa  bbb  ccc\nd"
		if got != want {
			t.Errorf("Table() = %q, want %q", got, want)
		}
	})
}

func TestRelativeTo(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		ago  time.Duration
		want string
	}{
		{"seconds round down to just now", 30 * time.Second, "just now"},
		{"exactly a minute", time.Minute, "1 minute ago"},
		{"plural minutes", 5 * time.Minute, "5 minutes ago"},
		{"hours", 2 * time.Hour, "2 hours ago"},
		{"a day and change is one day", 26 * time.Hour, "1 day ago"},
		{"plural days", 3 * 24 * time.Hour, "3 days ago"},
		{"a week", 8 * 24 * time.Hour, "1 week ago"},
		{"plural weeks", 20 * 24 * time.Hour, "2 weeks ago"},
		{"a month", 45 * 24 * time.Hour, "1 month ago"},
		{"plural months", 65 * 24 * time.Hour, "2 months ago"},
		{"a year", 400 * 24 * time.Hour, "1 year ago"},
		{"plural years", 800 * 24 * time.Hour, "2 years ago"},
		{"a future timestamp is not a negative age", -10 * time.Minute, "just now"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RelativeTo(now.Add(-tt.ago), now); got != tt.want {
				t.Errorf("RelativeTo(-%s) = %q, want %q", tt.ago, got, tt.want)
			}
		})
	}
}

func TestRelativeTime(t *testing.T) {
	t.Run("empty input stays empty", func(t *testing.T) {
		if got := RelativeTime(""); got != "" {
			t.Errorf("RelativeTime(\"\") = %q, want empty", got)
		}
	})
	t.Run("an unparseable value is echoed rather than swallowed", func(t *testing.T) {
		if got := RelativeTime("soon"); got != "soon" {
			t.Errorf("RelativeTime(\"soon\") = %q, want %q", got, "soon")
		}
	})
	t.Run("a real timestamp is relative", func(t *testing.T) {
		iso := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
		if got := RelativeTime(iso); got != "2 hours ago" {
			t.Errorf("RelativeTime(%s) = %q, want %q", iso, got, "2 hours ago")
		}
	})
}

func TestParseTime(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		ok    bool
		wantU int64 // unix milliseconds, checked only when ok
	}{
		{name: "empty", in: "", ok: false},
		{name: "whitespace only", in: "   ", ok: false},
		{
			name: "RFC3339 with zone", in: "2026-01-02T03:04:05Z", ok: true,
			wantU: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).UnixMilli(),
		},
		{
			name: "RFC3339 with fractional seconds", in: "2026-01-02T03:04:05.123Z", ok: true,
			wantU: time.Date(2026, 1, 2, 3, 4, 5, 123e6, time.UTC).UnixMilli(),
		},
		{
			name: "RFC3339 with a numeric offset", in: "2026-01-02T03:04:05+02:00", ok: true,
			wantU: time.Date(2026, 1, 2, 1, 4, 5, 0, time.UTC).UnixMilli(),
		},
		{
			name: "Bitbucket-style offset without a colon", in: "2026-01-02T03:04:05.123-0700", ok: true,
			wantU: time.Date(2026, 1, 2, 10, 4, 5, 123e6, time.UTC).UnixMilli(),
		},
		{
			name: "no zone at all is read as UTC", in: "2026-01-02T03:04:05", ok: true,
			wantU: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).UnixMilli(),
		},
		{
			name: "space separated", in: "2026-01-02 03:04:05", ok: true,
			wantU: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).UnixMilli(),
		},
		{
			name: "date only", in: "2026-01-02", ok: true,
			wantU: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC).UnixMilli(),
		},
		{
			name: "epoch milliseconds (13 digits)", in: "1700000000000", ok: true,
			wantU: 1700000000000,
		},
		{
			name: "epoch seconds (10 digits)", in: "1700000000", ok: true,
			wantU: 1700000000000,
		},
		{name: "too few digits to be an epoch", in: "170000000", ok: false},
		{name: "not a timestamp at all", in: "yesterday", ok: false},
		{name: "digits with a suffix", in: "1700000000000ms", ok: false},
		{
			name: "surrounding whitespace is trimmed", in: "  2026-01-02  ", ok: true,
			wantU: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC).UnixMilli(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseTime(tt.in)
			if ok != tt.ok {
				t.Fatalf("ParseTime(%q) ok = %v, want %v", tt.in, ok, tt.ok)
			}
			if ok && got.UnixMilli() != tt.wantU {
				t.Errorf("ParseTime(%q) = %s (%d ms), want %d ms",
					tt.in, got.UTC(), got.UnixMilli(), tt.wantU)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"shorter than the limit is untouched", "abc", 10, "abc"},
		{"exactly the limit keeps every rune", "abcde", 5, "abcde"},
		{"past the limit gets an ellipsis", "abcdefghij", 5, "abcd…"},
		{"internal whitespace is collapsed", "a   b\n\tc", 10, "a b c"},
		{"leading and trailing whitespace is dropped", "  hello  ", 10, "hello"},
		{"collapsing can bring it under the limit", "a     b", 3, "a b"},
		{"multibyte runes are counted, not bytes", "日本語テキスト", 4, "日本語…"},
		{"an emoji counts as one rune", "🙂🙂🙂🙂", 2, "🙂…"},
		{"a limit of one has no room for an ellipsis", "abc", 1, "a"},
		{"a limit of zero yields nothing", "abc", 0, ""},
		{"an empty string stays empty", "", 5, ""},
		{"whitespace only collapses to empty", "   \n  ", 5, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Truncate(tt.in, tt.n); got != tt.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
			}
		})
	}
}

func TestHumanSize(t *testing.T) {
	tests := []struct {
		name string
		in   int64
		want string
	}{
		{"zero renders nothing", 0, ""},
		{"a negative size renders nothing", -1, ""},
		{"bytes have no decimal", 512, "512 B"},
		{"just under a kilobyte", 1023, "1023 B"},
		{"one kilobyte", 1024, "1.0 KB"},
		{"one and a half kilobytes", 1536, "1.5 KB"},
		{"two kilobytes", 2048, "2.0 KB"},
		{"megabytes", 3 * 1024 * 1024, "3.0 MB"},
		{"gigabytes", 5 * 1024 * 1024 * 1024, "5.0 GB"},
		{"the ladder stops at GB", 1024 * 1024 * 1024 * 1024, "1024.0 GB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HumanSize(tt.in); got != tt.want {
				t.Errorf("HumanSize(%d) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCount(t *testing.T) {
	tests := []struct {
		name string
		n    int
		want string
	}{
		{"zero is plural", 0, "0 pull requests"},
		{"one is singular and spelled out as 1", 1, "1 pull request"},
		{"many is plural", 7, "7 pull requests"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Count(tt.n, "pull request", "pull requests"); got != tt.want {
				t.Errorf("Count(%d) = %q, want %q", tt.n, got, tt.want)
			}
		})
	}
}

func TestPlural(t *testing.T) {
	if got := Plural(1, "", "s"); got != "" {
		t.Errorf("Plural(1) = %q, want %q", got, "")
	}
	if got := Plural(2, "", "s"); got != "s" {
		t.Errorf("Plural(2) = %q, want %q", got, "s")
	}
	if got := Plural(0, "", "s"); got != "s" {
		t.Errorf("Plural(0) = %q, want %q", got, "s")
	}
}

func TestVisibleLenAndStripANSI(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		visible int
		bare    string
	}{
		{"plain text", "Done", 4, "Done"},
		{"empty", "", 0, ""},
		{"a wrapped color code", "\x1b[32mDone\x1b[0m", 4, "Done"},
		{"bold plus color", "\x1b[1m\x1b[31mfail\x1b[0m", 4, "fail"},
		{"a compound SGR sequence", "\x1b[1;31;40mx\x1b[0m", 1, "x"},
		{"a bare reset", "\x1b[mx", 1, "x"},
		{"multibyte content is counted in runes", "日本語", 3, "日本語"},
		{"color around multibyte content", "\x1b[36m日本\x1b[0m", 2, "日本"},
		{"color between words", "a\x1b[33mb\x1b[0mc", 3, "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VisibleLen(tt.in); got != tt.visible {
				t.Errorf("VisibleLen(%q) = %d, want %d", tt.in, got, tt.visible)
			}
			if got := StripANSI(tt.in); got != tt.bare {
				t.Errorf("StripANSI(%q) = %q, want %q", tt.in, got, tt.bare)
			}
		})
	}
}

func TestColorWrapping(t *testing.T) {
	t.Run("color off passes the string through untouched", func(t *testing.T) {
		noColor(t)
		if got := Green("ok"); got != "ok" {
			t.Errorf("Green(ok) with color off = %q, want %q", got, "ok")
		}
	})
	t.Run("color on wraps but does not change the visible length", func(t *testing.T) {
		prev := useColor
		SetColor(true)
		t.Cleanup(func() { useColor = prev })
		got := Green("ok")
		if got == "ok" {
			t.Fatalf("Green(ok) with color on = %q, want escape codes", got)
		}
		if VisibleLen(got) != 2 {
			t.Errorf("VisibleLen(%q) = %d, want 2", got, VisibleLen(got))
		}
		if StripANSI(got) != "ok" {
			t.Errorf("StripANSI(%q) = %q, want %q", got, StripANSI(got), "ok")
		}
	})
}
