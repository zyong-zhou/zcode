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

func TestTruncateWidth(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"hello", 0, ""},
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello", 3, "he…"},
		{"hello", 1, "…"},
		{"你好世界", 5, "你好…"},
	}
	for _, tc := range cases {
		if got := truncateWidth(tc.in, tc.max); got != tc.want {
			t.Errorf("truncateWidth(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
}

func TestTruncateLines(t *testing.T) {
	if got := truncateLines("a\nb", 0, 5); got != "" {
		t.Errorf("zero lines = %q, want empty", got)
	}
	if got := truncateLines("a\nb\nc", 2, 5); got != "a\nb\n…" {
		t.Errorf("truncateLines = %q, want a\\nb\\n…", got)
	}
	if got := truncateLines("abcdef", 1, 3); got != "ab…" {
		t.Errorf("truncateLines width = %q, want ab…", got)
	}
}
