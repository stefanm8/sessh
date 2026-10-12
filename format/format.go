// Package format renders human-readable terminal output and machine-readable
// output when redirected:
//   - interactive TTY: aligned, colorized columns;
//   - piped or redirected: plain tab-separated values, no color, so
//     `sessh bitbucket repo list | cut -f1` just works.
package format

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

var isTTY = func() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}()

var useColor = isTTY && os.Getenv("NO_COLOR") == ""

// IsTTY reports whether stdout is a terminal.
func IsTTY() bool { return isTTY }

// SetColor forces color on or off (tests, --no-color).
func SetColor(on bool) { useColor = on }

func wrap(code, s string) string {
	if !useColor {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func Bold(s string) string    { return wrap("1", s) }
func Red(s string) string     { return wrap("31", s) }
func Green(s string) string   { return wrap("32", s) }
func Yellow(s string) string  { return wrap("33", s) }
func Blue(s string) string    { return wrap("34", s) }
func Magenta(s string) string { return wrap("35", s) }
func Cyan(s string) string    { return wrap("36", s) }
func Gray(s string) string    { return wrap("90", s) }

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// VisibleLen is the length ignoring ANSI escapes, so padding stays correct
// with color on.
func VisibleLen(s string) int { return utf8.RuneCountInString(ansiRe.ReplaceAllString(s, "")) }

// StripANSI removes color codes — file output is never colorized.
func StripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// Table renders rows as aligned columns on a TTY and as tab-separated values
// when piped. Headers are printed only on a TTY.
func Table(rows [][]string, columns []string) string {
	if len(rows) == 0 {
		return ""
	}
	if !isTTY {
		lines := make([]string, 0, len(rows))
		for _, r := range rows {
			lines = append(lines, strings.Join(r, "\t"))
		}
		return strings.Join(lines, "\n")
	}

	widths := map[int]int{}
	measure := func(row []string) {
		for i, c := range row {
			if n := VisibleLen(c); n > widths[i] {
				widths[i] = n
			}
		}
	}
	if columns != nil {
		measure(columns)
	}
	for _, r := range rows {
		measure(r)
	}

	fmtRow := func(row []string) string {
		var b strings.Builder
		for i, c := range row {
			if i > 0 {
				b.WriteString("  ")
			}
			b.WriteString(c)
			if i != len(row)-1 {
				if pad := widths[i] - VisibleLen(c); pad > 0 {
					b.WriteString(strings.Repeat(" ", pad))
				}
			}
		}
		return b.String()
	}

	var lines []string
	if columns != nil {
		upper := make([]string, len(columns))
		for i, c := range columns {
			upper[i] = strings.ToUpper(c)
		}
		lines = append(lines, Gray(fmtRow(upper)))
	}
	for _, r := range rows {
		lines = append(lines, fmtRow(r))
	}
	return strings.Join(lines, "\n")
}

// RelativeTime is gh-style: "2 minutes ago", "5 days ago".
func RelativeTime(iso string) string {
	if iso == "" {
		return ""
	}
	t, ok := ParseTime(iso)
	if !ok {
		return iso
	}
	return RelativeTo(t, time.Now())
}

// RelativeTo formats the gap between two instants.
func RelativeTo(t, now time.Time) string {
	secs := int64(math.Round(now.Sub(t).Seconds()))
	if secs < 0 {
		return "just now"
	}
	units := []struct {
		name string
		size int64
	}{
		{"year", 31536000}, {"month", 2592000}, {"week", 604800},
		{"day", 86400}, {"hour", 3600}, {"minute", 60},
	}
	for _, u := range units {
		if n := secs / u.size; n >= 1 {
			if n == 1 {
				return fmt.Sprintf("1 %s ago", u.name)
			}
			return fmt.Sprintf("%d %ss ago", n, u.name)
		}
	}
	return "just now"
}

// ParseTime accepts the timestamp shapes these APIs actually return: RFC3339
// with or without fractional seconds and with or without a zone, plus
// epoch-milliseconds as a string.
func ParseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999-0700",
		"2006-01-02T15:04:05.999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	// Epoch milliseconds (Teams writes these).
	if isDigits(s) && len(s) >= 10 {
		var ms int64
		if _, err := fmt.Sscanf(s, "%d", &ms); err == nil {
			if len(s) >= 13 {
				return time.UnixMilli(ms), true
			}
			return time.Unix(ms, 0), true
		}
	}
	return time.Time{}, false
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// Truncate collapses whitespace and cuts to n runes with an ellipsis.
func Truncate(s string, n int) string {
	clean := strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(clean) <= n {
		return clean
	}
	runes := []rune(clean)
	if n <= 1 {
		return string(runes[:n])
	}
	return string(runes[:n-1]) + "…"
}

// PrintJSON writes an indented JSON document to stdout.
func PrintJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// Line prints a label/value pair, skipping empty values.
func Line(label, value string, width int) {
	if strings.TrimSpace(value) == "" {
		return
	}
	pad := width - len(label)
	if pad < 0 {
		pad = 0
	}
	fmt.Println(Gray(label+strings.Repeat(" ", pad)) + " " + value)
}

// Plural returns the singular or plural suffix for n.
func Plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// Count renders "3 things" / "1 thing".
func Count(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// HumanSize renders a byte count.
func HumanSize(n int64) string {
	if n <= 0 {
		return ""
	}
	units := []string{"B", "KB", "MB", "GB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d %s", int64(v), units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// OpenInBrowser opens a URL in the user's default browser — gh's `--web`
// behavior. Nothing about this touches a sessh profile; it is the ordinary
// desktop browser.
func OpenInBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Println(url)
		return fmt.Errorf("could not open a browser: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	fmt.Println(Gray("Opening ") + url + Gray(" in your browser..."))
	return nil
}
