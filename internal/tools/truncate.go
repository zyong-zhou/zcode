package tools

import (
	"fmt"
	"unicode/utf8"
)

// defaultMaxBytes caps how much text a tool feeds back to the model.
const defaultMaxBytes = 64 << 10 // 64 KiB

// truncate keeps the head and tail of s and replaces the middle with a marker
// when s exceeds max bytes. Cuts are aligned to rune boundaries so the result
// stays valid UTF-8. A non-positive max means defaultMaxBytes.
func truncate(s string, max int) string {
	if max <= 0 {
		max = defaultMaxBytes
	}
	if len(s) <= max {
		return s
	}

	half := max / 2

	// A prefix is valid UTF-8 when it ends just before a rune-start byte, so
	// back off from s[:half] while the first excluded byte is a continuation.
	n := half
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	head := s[:n]

	// Likewise, s[m:] is valid when s[m] is a rune start.
	m := len(s) - half
	for m < len(s) && !utf8.RuneStart(s[m]) {
		m++
	}
	tail := s[m:]

	omitted := len(s) - len(head) - len(tail)
	return head + fmt.Sprintf("\n... [%d bytes omitted] ...\n", omitted) + tail
}

// Truncate is the exported form of truncate, for tools implemented in other
// packages (for example the MCP tool adapter).
func Truncate(s string, max int) string { return truncate(s, max) }
