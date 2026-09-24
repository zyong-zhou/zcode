package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotAndRestore(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "a.txt")
	if err := os.WriteFile(src, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Store{Dir: filepath.Join(root, ".zcode", "backups"), Root: root}

	id, err := s.Snapshot("a.txt", src)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if id == "" {
		t.Fatal("Snapshot returned an empty id for an existing file")
	}
	if err := os.WriteFile(src, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}

	entry, err := s.Restore(id)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if entry.Source != "a.txt" {
		t.Errorf("entry.Source = %q, want a.txt", entry.Source)
	}
	got, _ := os.ReadFile(src)
	if string(got) != "v1" {
		t.Errorf("restored content = %q, want v1", got)
	}
}

func TestSnapshotNewFileIsNoop(t *testing.T) {
	root := t.TempDir()
	s := Store{Dir: filepath.Join(root, ".zcode", "backups"), Root: root}
	id, err := s.Snapshot("nope.txt", filepath.Join(root, "nope.txt"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if id != "" {
		t.Errorf("id = %q, want empty for a missing file", id)
	}
}

func TestSnapshotDisabled(t *testing.T) {
	id, err := (Store{}).Snapshot("a.txt", "a.txt")
	if err != nil || id != "" {
		t.Errorf("disabled store Snapshot = (%q, %v), want empty/nil", id, err)
	}
}

func TestLatestReturnsNewest(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "a.txt")
	if err := os.WriteFile(src, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Store{Dir: filepath.Join(root, "b"), Root: root}
	if _, err := s.Snapshot("a.txt", src); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Snapshot("a.txt", src); err != nil {
		t.Fatal(err)
	}

	entry, ok, err := s.Latest()
	if err != nil || !ok {
		t.Fatalf("Latest = (%v, %v)", ok, err)
	}
	if entry.Source != "a.txt" {
		t.Errorf("Latest source = %q", entry.Source)
	}
	list, err := s.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %d entries, err=%v; want 2", len(list), err)
	}
	if !(list[0].ID > list[1].ID) {
		t.Errorf("List not newest-first: %q then %q", list[0].ID, list[1].ID)
	}
}

func TestRestoreRejectsEscape(t *testing.T) {
	root := t.TempDir()
	s := Store{Dir: filepath.Join(root, "b"), Root: root}
	id := "1-" + encodeSource("../evil.txt")
	if _, err := s.Restore(id); err == nil {
		t.Error("Restore accepted a path escaping the root")
	}
}

func TestListMissingDir(t *testing.T) {
	list, err := (Store{Dir: filepath.Join(t.TempDir(), "none")}).List()
	if err != nil || list != nil {
		t.Errorf("List on missing dir = (%v, %v), want nil/nil", list, err)
	}
}
