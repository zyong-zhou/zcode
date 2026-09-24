package tui

import "testing"

func TestWrapText(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{"short stays", "hello world", 20, "hello world"},
		{"breaks after exact fit", "hello world foo", 11, "hello world\nfoo"},
		{"breaks at last space", "hello world foo", 9, "hello\nworld foo"},
		{"hard break long word", "abcdefghij", 4, "abcd\nefgh\nij"},
		{"keeps newlines", "a\nb\nc", 10, "a\nb\nc"},
		{"cjk width", "你好世界", 4, "你好\n世界"},
		{"cjk mixed width", "ab你好", 4, "ab你\n好"},
		{"zero width is clamped", "ab", 0, "a\nb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wrapText(tt.in, tt.width); got != tt.want {
				t.Errorf("wrapText(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
			}
		})
	}
}
