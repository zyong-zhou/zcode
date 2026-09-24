package tools

import (
	"strings"
	"testing"
)

func TestWriteFilePreview(t *testing.T) {
	p, ok := (WriteFile{Root: "."}).Preview(`{"path":"a.txt","content":"line1\nline2"}`)
	if !ok {
		t.Fatal("expected a preview")
	}
	if !strings.Contains(p, "a.txt") || !strings.Contains(p, "line1") {
		t.Errorf("preview = %q, want the path and content head", p)
	}
}

func TestWriteFilePreviewLargeContentIsCapped(t *testing.T) {
	body := strings.Repeat("x\n", previewMaxLines+5)
	p, ok := (WriteFile{Root: "."}).Preview(`{"path":"a.txt","content":` + quote(body) + `}`)
	if !ok {
		t.Fatal("expected a preview")
	}
	if strings.Count(p, "\n") > previewMaxLines+1 {
		t.Errorf("preview not capped:\n%s", p)
	}
}

func TestEditPreview(t *testing.T) {
	p, ok := (Edit{Root: "."}).Preview(`{"path":"a.go","edits":[{"old":"foo","new":"bar"}]}`)
	if !ok {
		t.Fatal("expected a preview")
	}
	if !strings.Contains(p, "a.go") || !strings.Contains(p, "- foo") || !strings.Contains(p, "+ bar") {
		t.Errorf("preview = %q, want the path and the old/new lines", p)
	}
}

func TestPreviewUnsupportedToolIsEmpty(t *testing.T) {
	if p := Preview(ListDir{Root: "."}, `{}`); p != "" {
		t.Errorf("ListDir preview = %q, want empty", p)
	}
}

func TestRunCommandPreview(t *testing.T) {
	p, ok := (RunCommand{}).Preview(`{"command":"go","args":["test","./..."]}`)
	if !ok {
		t.Fatal("expected a preview")
	}
	if !strings.Contains(p, "go test ./...") {
		t.Errorf("preview = %q, want the full command line", p)
	}
	if _, ok := (RunCommand{}).Preview(`{"command":"  "}`); ok {
		t.Error("blank command should have no preview")
	}
}

func TestPreviewBadArgsIsEmpty(t *testing.T) {
	if p := Preview(WriteFile{Root: "."}, `{bad json`); p != "" {
		t.Errorf("preview = %q, want empty for bad args", p)
	}
}

func TestPreviewTruncateWidth(t *testing.T) {
	// A non-positive width means "do not truncate".
	if got := truncateWidth("abc", 0); got != "abc" {
		t.Errorf("truncateWidth(abc, 0) = %q, want abc", got)
	}
	if got := truncateWidth("abc", 3); got != "abc" {
		t.Errorf("truncateWidth(abc, 3) = %q, want abc", got)
	}
	if got := truncateWidth("abcd", 2); got != "ab…" {
		t.Errorf("truncateWidth(abcd, 2) = %q, want ab…", got)
	}
}

func TestOneLineCollapsesWhitespace(t *testing.T) {
	if got := oneLine("  a \n\t b  ", 100); got != "a b" {
		t.Errorf("oneLine = %q, want a b", got)
	}
}

// quote JSON-encodes s as a string literal.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\n':
			b.WriteString(`\n`)
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
