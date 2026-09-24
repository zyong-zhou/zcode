package rules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConcatenates(t *testing.T) {
	root := t.TempDir()
	write(t, root, "AGENTS.md", "# 约定\n用 gofmt。")
	write(t, root, "EXTRA.md", "  \n额外规则。\n")

	s, err := Load(root, []string{"AGENTS.md", "EXTRA.md", "MISSING.md"}, 0)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	out := s.Render()
	if !strings.Contains(out, "用 gofmt") || !strings.Contains(out, "额外规则") {
		t.Errorf("render = %q", out)
	}
	if len(s.Files()) != 2 {
		t.Errorf("files = %v, want the two that exist", s.Files())
	}
}

func TestLoadEmptyAndMissing(t *testing.T) {
	s, err := Load(t.TempDir(), []string{"AGENTS.md"}, 0)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Render() != "" || len(s.Files()) != 0 {
		t.Errorf("expected nothing loaded, got %q / %v", s.Render(), s.Files())
	}
}

func TestLoadTruncatesRuneSafe(t *testing.T) {
	root := t.TempDir()
	write(t, root, "AGENTS.md", strings.Repeat("规", 100))
	s, err := Load(root, []string{"AGENTS.md"}, 10)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	out := s.Render()
	if !strings.Contains(out, "[rules truncated]") {
		t.Errorf("missing truncation marker: %q", out)
	}
	if !strings.HasPrefix(out, strings.Repeat("规", 10)) {
		t.Errorf("unexpected prefix: %q", out)
	}
}
