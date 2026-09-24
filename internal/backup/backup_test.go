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

func TestSnapshotPrunesToKeep(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "a.txt")
	s := Store{Dir: filepath.Join(root, "b"), Root: root, Keep: 2}

	for i := 0; i < 4; i++ {
		if err := os.WriteFile(src, []byte("v"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Snapshot("a.txt", src); err != nil {
			t.Fatalf("Snapshot %d: %v", i, err)
		}
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("retained %d snapshots, want 2 (Keep)", len(list))
	}
}

func TestDropRemovesSnapshot(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "a.txt")
	if err := os.WriteFile(src, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Store{Dir: filepath.Join(root, "b"), Root: root}
	id, err := s.Snapshot("a.txt", src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Drop(id); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if list, _ := s.List(); len(list) != 0 {
		t.Errorf("after Drop List = %d, want 0", len(list))
	}
	// Dropping again is a no-op, but a malformed id is rejected.
	if err := s.Drop(id); err != nil {
		t.Errorf("second Drop = %v, want nil", err)
	}
	if err := s.Drop("noid"); err == nil {
		t.Error("Drop accepted a malformed id")
	}
}

func TestSnapshotIDsAreUnique(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "a.txt")
	if err := os.WriteFile(src, []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Store{Dir: filepath.Join(root, "b"), Root: root}

	// Two snapshots taken back-to-back must not collide, even when the clock
	// returns the same nanosecond (coarse Windows timer).
	id1, err := s.Snapshot("a.txt", src)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.Snapshot("a.txt", src)
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatalf("snapshot ids collide: %q", id1)
	}
	list, err := s.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %d entries, err=%v; want 2", len(list), err)
	}
	for _, e := range list {
		if e.Source != "a.txt" {
			t.Errorf("entry source = %q, want a.txt", e.Source)
		}
	}
}

func TestResolveRejectsBadPaths(t *testing.T) {
	root := t.TempDir()
	got, err := resolve(root, "a/b.txt")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("resolve = %q, want an absolute path", got)
	}
	if _, err := resolve(root, "  "); err == nil {
		t.Error("resolve should reject an empty path")
	}
	if _, err := resolve(root, "../evil"); err == nil {
		t.Error("resolve should reject a path escaping the root")
	}
}
