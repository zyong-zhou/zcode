package tools

import (
	"fmt"
	"strings"
)

// Preview formatting bounds. They keep an approval prompt short even when the
// model is about to write a large file.
const (
	previewMaxLines = 12
	previewMaxWidth = 160
)

// firstLines returns the first maxLines lines of s, each cut to maxWidth runes,
// appending a marker when lines were dropped.
func firstLines(s string, maxLines, maxWidth int) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	shown := lines
	if len(lines) > maxLines {
		shown = lines[:maxLines]
	}
	var b strings.Builder
	for i, ln := range shown {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(truncateWidth(ln, maxWidth))
	}
	if len(lines) > maxLines {
		fmt.Fprintf(&b, "\n…（还有 %d 行）", len(lines)-maxLines)
	}
	return b.String()
}

// oneLine flattens s to a single whitespace-collapsed line cut to maxWidth.
func oneLine(s string, maxWidth int) string {
	return truncateWidth(strings.Join(strings.Fields(s), " "), maxWidth)
}

// truncateWidth cuts s to at most max runes, appending an ellipsis when cut.
func truncateWidth(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
