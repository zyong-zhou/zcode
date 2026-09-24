package memory

import (
	"path/filepath"
	"testing"
)

func TestOpenEmptyPathFails(t *testing.T) {
	if _, err := Open("   "); err == nil {
		t.Error("Open with an empty path should fail")
	}
}

func TestStorePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".zcode", "memory.md")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Path() != path {
		t.Errorf("Path() = %q, want %q", s.Path(), path)
	}
}

func TestSearchMatchesTextAndTags(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.md"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Add("项目统一用 gofmt", []string{"Go", "Style"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Add("部署走 goproxy.cn", []string{"proxy"}); err != nil {
		t.Fatal(err)
	}

	// Text match.
	if got := s.Search("gofmt", 10); len(got) != 1 {
		t.Errorf("text search = %d entries, want 1", len(got))
	}
	// Tag match is case-insensitive.
	if got := s.Search("style", 10); len(got) != 1 {
		t.Errorf("tag search = %d entries, want 1", len(got))
	}
	// Empty query returns everything (up to the limit).
	if got := s.Search("", 10); len(got) != 2 {
		t.Errorf("empty search = %d entries, want 2", len(got))
	}
	// No match.
	if got := s.Search("rust", 10); len(got) != 0 {
		t.Errorf("non-matching search = %d entries, want 0", len(got))
	}
}

func TestHasTag(t *testing.T) {
	if !hasTag([]string{"Go", "Style"}, "go") {
		t.Error("hasTag should match case-insensitively and by substring")
	}
	if hasTag([]string{"Go"}, "rust") {
		t.Error("hasTag should not match unrelated tags")
	}
	if hasTag(nil, "x") {
		t.Error("hasTag on no tags should be false")
	}
}

func TestEnforceDropsOldestEntries(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.md"))
	if err != nil {
		t.Fatal(err)
	}
	s.maxEntries = 2
	for _, txt := range []string{"one", "two", "three"} {
		if _, _, err := s.Add(txt, nil); err != nil {
			t.Fatal(err)
		}
	}
	entries := s.Entries()
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	for _, e := range entries {
		if e.Text == "one" {
			t.Error("the oldest entry should have been dropped")
		}
	}

	// The byte cap must always keep at least one entry.
	s2, err := Open(filepath.Join(t.TempDir(), "m2.md"))
	if err != nil {
		t.Fatal(err)
	}
	s2.maxBytes = 1
	for _, txt := range []string{"hello", "world"} {
		if _, _, err := s2.Add(txt, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(s2.Entries()); got != 1 {
		t.Errorf("byte-capped entries = %d, want 1", got)
	}
}
