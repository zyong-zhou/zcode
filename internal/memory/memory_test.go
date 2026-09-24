package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddDedupPersistRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".zcode", "memory.md")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, added, err := s.Add("使用 Go 1.26", []string{"toolchain"}); err != nil || !added {
		t.Fatalf("Add: added=%v err=%v", added, err)
	}
	if _, added, _ := s.Add("  使用 Go 1.26  ", nil); added {
		t.Error("a duplicate should not be added")
	}
	if s.Count() != 1 {
		t.Fatalf("count = %d, want 1", s.Count())
	}

	// Reopen to prove the fact was persisted.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if s2.Count() != 1 {
		t.Fatalf("reloaded count = %d, want 1", s2.Count())
	}
	if !strings.Contains(s2.Render(), "使用 Go 1.26") {
		t.Errorf("render = %q", s2.Render())
	}
	if got := s2.Search("go", 0); len(got) != 1 {
		t.Errorf("search = %v", got)
	}

	if n := s2.Remove("Go 1.26"); n != 1 {
		t.Errorf("removed = %d, want 1", n)
	}
	if s2.Count() != 0 {
		t.Errorf("count after remove = %d", s2.Count())
	}
}

func TestParseHumanEditedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.md")
	body := "# 项目记忆\n\n- (2026-09-24) [架构] 使用 bubbletea\n- 手写的一行\nnot a bullet\n"
	if err := writeFile(t, path, body); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	entries := s.Entries()
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2: %+v", len(entries), entries)
	}
	if entries[0].Text != "使用 bubbletea" || len(entries[0].Tags) != 1 {
		t.Errorf("entry 0 = %+v", entries[0])
	}
	if entries[1].Text != "手写的一行" {
		t.Errorf("entry 1 = %+v", entries[1])
	}
}

func TestClear(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = s.Add("a", nil)
	_, _, _ = s.Add("b", nil)
	if n := s.Clear(); n != 2 {
		t.Errorf("clear = %d, want 2", n)
	}
	if s.Count() != 0 {
		t.Errorf("count = %d, want 0", s.Count())
	}
}

func writeFile(t *testing.T, path, content string) error {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
