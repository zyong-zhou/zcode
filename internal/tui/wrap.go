package tui

import (
	"strings"

	"github.com/mattn/go-runewidth"
)

// truncateWidth cuts s to at most max display columns, appending an ellipsis
// when it had to drop something. It is rune-width aware for CJK text.
func truncateWidth(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if w+rw > max-1 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String() + "…"
}

// truncateLines cuts s to at most maxLines lines and width columns per line,
// appending a marker when lines were dropped.
func truncateLines(s string, maxLines, width int) string {
	if maxLines <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	shown := lines
	if len(lines) > maxLines {
		shown = lines[:maxLines]
	}
	out := make([]string, 0, len(shown))
	for _, ln := range shown {
		out = append(out, truncateWidth(ln, width))
	}
	res := strings.Join(out, "\n")
	if len(lines) > maxLines {
		res += "\n…"
	}
	return res
}

// wrapText soft-wraps s to width display columns. Explicit newlines are kept,
// and lines break at a space when one is available (falling back to a hard
// break). Widths are measured with rune widths so CJK text lines up in the
// terminal instead of overflowing.
func wrapText(s string, width int) string {
	if width < 1 {
		width = 1
	}
	var out strings.Builder
	for pi, para := range strings.Split(s, "\n") {
		if pi > 0 {
			out.WriteByte('\n')
		}
		runes := []rune(para)
		for len(runes) > 0 {
			w, n := 0, 0
			for n < len(runes) {
				rw := runewidth.RuneWidth(runes[n])
				if w+rw > width {
					break
				}
				w += rw
				n++
			}
			if n == 0 {
				n = 1 // a single rune wider than the line
			}
			if n >= len(runes) {
				out.WriteString(string(runes))
				break
			}
			// Prefer breaking right after a word that exactly fills the line
			// (dropping the following space); otherwise break at the last space
			// that fits, or hard-break.
			brk, skip := n, 0
			if runes[n] == ' ' || runes[n] == '\t' {
				skip = 1
			} else {
				for i := n - 1; i > 0; i-- {
					if runes[i] == ' ' || runes[i] == '\t' {
						brk = i
						break
					}
				}
			}
			out.WriteString(string(runes[:brk]))
			out.WriteByte('\n')
			rest := runes[brk+skip:]
			for len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t') {
				rest = rest[1:]
			}
			runes = rest
		}
	}
	return out.String()
}
