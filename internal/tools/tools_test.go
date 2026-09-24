package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWriteThenReadFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := (WriteFile{Root: dir}).Run(context.Background(), `{"path":"sub/a.txt","content":"hello"}`); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := (ReadFile{Root: dir}).Run(context.Background(), `{"path":"sub/a.txt"}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "hello" {
		t.Errorf("got %q, want hello", got)
	}
}

func TestReadFileRejectsEscape(t *testing.T) {
	r := ReadFile{Root: t.TempDir()}
	if _, err := r.Run(context.Background(), `{"path":"../secret"}`); err == nil {
		t.Fatal("expected error for path escaping root")
	}
}

func TestWriteFileRejectsEscape(t *testing.T) {
	w := WriteFile{Root: t.TempDir()}
	if _, err := w.Run(context.Background(), `{"path":"../escape.txt","content":"x"}`); err == nil {
		t.Fatal("expected error for path escaping root")
	}
}

func TestRunCommand(t *testing.T) {
	out, err := (RunCommand{Dir: t.TempDir()}).Run(context.Background(), `{"command":"go","args":["version"]}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "go version") {
		t.Errorf("output = %q, want it to contain %q", out, "go version")
	}
}

func TestListDir(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "x")
	mustWrite(t, filepath.Join(dir, "sub", "b.go"), "package sub")
	mustWrite(t, filepath.Join(dir, ".git", "config"), "secret")

	out, err := (ListDir{Root: dir}).Run(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "a.txt") || !strings.Contains(out, "sub/") || !strings.Contains(out, "sub/b.go") {
		t.Errorf("list output = %q", out)
	}
	if strings.Contains(out, ".git") {
		t.Errorf("list output should skip .git: %q", out)
	}
}

func TestListDirMaxDepth(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a", "b", "c.txt"), "x")

	out, err := (ListDir{Root: dir}).Run(context.Background(), `{"max_depth":1}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "a/") {
		t.Errorf("depth 1 should include first-level dir: %q", out)
	}
	if strings.Contains(out, "c.txt") {
		t.Errorf("depth 1 should not include nested file: %q", out)
	}
}

func TestListDirRejectsEscape(t *testing.T) {
	if _, err := (ListDir{Root: t.TempDir()}).Run(context.Background(), `{"path":".."}`); err == nil {
		t.Fatal("expected error for path escaping root")
	}
}

func TestSearch(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.go"), "package main\nfunc hello() {}\n")
	mustWrite(t, filepath.Join(dir, "b.txt"), "nothing here\n")

	out, err := (Search{Root: dir}).Run(context.Background(), `{"pattern":"hello"}`)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(out, "a.go:2:") || !strings.Contains(out, "hello") {
		t.Errorf("search output = %q", out)
	}
	if strings.Contains(out, "b.txt") {
		t.Errorf("unexpected match in b.txt: %q", out)
	}
}

func TestSearchInclude(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.go"), "hello\n")
	mustWrite(t, filepath.Join(dir, "a.txt"), "hello\n")

	out, err := (Search{Root: dir}).Run(context.Background(), `{"pattern":"hello","include":"*.go"}`)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(out, "a.go") || strings.Contains(out, "a.txt") {
		t.Errorf("include filter failed: %q", out)
	}
}

func TestSearchMaxResults(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "hit\nhit\nhit\n")

	out, err := (Search{Root: dir}).Run(context.Background(), `{"pattern":"hit","max_results":2}`)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got := strings.Count(out, "a.txt:"); got != 2 {
		t.Errorf("matches = %d, want 2: %q", got, out)
	}
	if !strings.Contains(out, "stopped at 2 matches") {
		t.Errorf("want truncation note: %q", out)
	}
}

func TestSearchRejectsBadPattern(t *testing.T) {
	if _, err := (Search{Root: t.TempDir()}).Run(context.Background(), `{"pattern":"("}`); err == nil {
		t.Fatal("expected error for invalid regexp")
	}
}

func TestReadFileTruncates(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "big.txt"), strings.Repeat("A", 1000))

	out, err := (ReadFile{Root: dir}).Run(context.Background(), `{"path":"big.txt","max_bytes":100}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, "bytes omitted") {
		t.Errorf("want truncation marker: %q", out)
	}
	if len(out) > 200 {
		t.Errorf("output too long: %d bytes", len(out))
	}
}

func TestRunCommandTruncates(t *testing.T) {
	out, err := (RunCommand{Dir: t.TempDir(), MaxOutputBytes: 64}).Run(context.Background(), `{"command":"go","args":["env"]}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "bytes omitted") {
		t.Errorf("want truncated output, got %d bytes", len(out))
	}
}

func TestTruncateRuneSafe(t *testing.T) {
	out := truncate(strings.Repeat("你好", 500), 100)
	if !utf8.ValidString(out) {
		t.Fatal("truncate produced invalid UTF-8")
	}
	if !strings.Contains(out, "bytes omitted") {
		t.Errorf("want truncation marker: %q", out)
	}
}

func TestRegistryOrderStable(t *testing.T) {
	a := ReadFile{Root: "."}
	b := WriteFile{Root: "."}
	r := NewRegistry(a)
	r.Register(b)
	r.Register(a) // replace must keep original position
	all := r.All()
	if len(all) != 2 || all[0].Name() != "read_file" || all[1].Name() != "write_file" {
		t.Fatalf("order = %v", names(all))
	}
}

func TestRegistryUnregister(t *testing.T) {
	r := NewRegistry(ReadFile{Root: "."}, WriteFile{Root: "."}, ListDir{Root: "."})

	r.Unregister("write_file")
	if _, ok := r.Get("write_file"); ok {
		t.Error("write_file should be gone")
	}
	if got := names(r.All()); len(got) != 2 || got[0] != "read_file" || got[1] != "list_dir" {
		t.Errorf("after unregister = %v", got)
	}

	r.Unregister("write_file") // no-op on an unknown name

	r.Register(WriteFile{Root: "."}) // re-adding appends at the end
	if got := names(r.All()); len(got) != 3 || got[2] != "write_file" {
		t.Errorf("after re-register = %v", got)
	}
}

func names(ts []Tool) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name()
	}
	return out
}
