package ui

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func pad(s string, w int) string {
	if w <= 0 {
		return ""
	}
	sw := lipgloss.Width(s)
	if sw > w {
		return ansi.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", w-sw)
}

func padLeft(s string, w int) string {
	sw := lipgloss.Width(s)
	if sw > w {
		return ansi.Truncate(s, w, "…")
	}
	return strings.Repeat(" ", w-sw) + s
}

// truncLeft keeps the last w cells of s, marking the cut with "…".
func truncLeft(s string, w int) string {
	sw := ansi.StringWidth(s)
	if sw <= w {
		return s
	}
	return "…" + ansi.TruncateLeft(s, sw-(w-1), "")
}

func fmtInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// clean makes database text safe to draw: control characters (which could
// carry terminal escape sequences) are replaced. With oneLine, line breaks
// become "⏎" and only the start of long values is kept, enough for a cell.
func clean(s string, oneLine bool) string {
	if oneLine && len(s) > maxCellBytes {
		cut := maxCellBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	if strings.IndexFunc(s, unicode.IsControl) < 0 {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' && oneLine:
			b.WriteRune('⏎')
		case r == '\n':
			b.WriteRune(r)
		case r == '\t':
			b.WriteByte(' ')
		case r == '\r':
		case unicode.IsControl(r):
			b.WriteRune('·')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// maxCellBytes bounds how much of a value clean looks at for a grid cell.
const maxCellBytes = 4 * maxColWidth

// smartMatch reports whether s contains needle, ignoring case unless needle
// has an upper-case letter.
func smartMatch(s, needle string) bool {
	if needle == "" {
		return true
	}
	if strings.ToLower(needle) != needle {
		return strings.Contains(s, needle)
	}
	return strings.Contains(strings.ToLower(s), needle)
}

// shortErr is the first line of an error, for the status bar.
func shortErr(err error) string {
	s, _, _ := strings.Cut(err.Error(), "\n")
	return clean(s, true)
}

// fmtDur rounds a duration to what is worth reading in a status line.
func fmtDur(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "<1ms"
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	}
	return d.Round(10 * time.Millisecond).String()
}
