package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// concTool is a minimal named tool for registry tests.
type concTool struct{ name string }

func (t concTool) Name() string                                { return t.name }
func (t concTool) Description() string                         { return "test tool" }
func (t concTool) Parameters() map[string]any                  { return map[string]any{"type": "object"} }
func (t concTool) Run(context.Context, string) (string, error) { return "", nil }

// MCP tool-list changes arrive on a notification goroutine while the agent loop
// reads the registry. An unsynchronised map makes that a fatal runtime error
// rather than a recoverable panic, so concurrent access must be safe. The race
// detector is what actually checks this.
func TestRegistryConcurrentAccess(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				name := fmt.Sprintf("t%d-%d", i, j)
				r.Register(concTool{name: name})
				_, _ = r.Get("t0-0")
				_ = r.All()
				r.Unregister(name)
			}
		}(i)
	}
	wg.Wait()
}

// makeLinkDir creates a directory link at link pointing to target. Windows
// symlinks need a privilege most users lack, but junctions do not, and a
// junction is the realistic way a link appears inside a checkout.
func makeLinkDir(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Symlink(target, link); err == nil {
		return
	} else if runtime.GOOS != "windows" {
		t.Skipf("cannot create symlinks in this environment: %v", err)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("cannot create a junction: %v: %s", err, out)
	}
}

// A link inside the root must not be a way out of it, either as an intermediate
// path element or as the target itself.
func TestFileToolsRejectLinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "secret.txt"), "TOP SECRET")

	makeLinkDir(t, filepath.Join(root, "link"), outside)

	if _, err := (ReadFile{Root: root}).Run(context.Background(), `{"path":"link/secret.txt"}`); err == nil {
		t.Error("read_file followed a link out of the project root")
	}
	if _, err := (ReadFile{Root: root}).Run(context.Background(), `{"path":"link"}`); err == nil {
		t.Error("read_file read a link pointing out of the project root")
	}
	if _, err := (WriteFile{Root: root}).Run(context.Background(), `{"path":"link/evil.txt","content":"x"}`); err == nil {
		t.Error("write_file wrote through a link out of the project root")
	}
	if _, err := (Edit{Root: root}).Run(context.Background(), `{"path":"link/secret.txt","edits":[{"old":"TOP","new":"X"}]}`); err == nil {
		t.Error("edit went through a link out of the project root")
	}
}

// The link check must not reject ordinary paths, including ones whose parent
// directories do not exist yet.
func TestFileToolsAllowNewNestedFile(t *testing.T) {
	root := t.TempDir()
	if _, err := (WriteFile{Root: root}).Run(context.Background(), `{"path":"a/b/c.txt","content":"hi"}`); err != nil {
		t.Fatalf("write nested: %v", err)
	}
	got, err := (ReadFile{Root: root}).Run(context.Background(), `{"path":"a/b/c.txt"}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "hi" {
		t.Errorf("content = %q, want %q", got, "hi")
	}
}

// cappedWriter keeps command output bounded no matter how much a runaway
// process writes, while preserving the head (what it did) and the tail (why it
// failed).
func TestCappedWriterBoundsMemory(t *testing.T) {
	const limit = 100
	w := &cappedWriter{limit: limit}

	// Write far more than the limit, in chunks, the way a process would.
	for i := 0; i < 1000; i++ {
		if _, err := w.Write([]byte("0123456789")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	got := w.String()
	if w.total != 10000 {
		t.Errorf("total = %d, want 10000", w.total)
	}
	if len(w.head)+len(w.tail) > limit {
		t.Errorf("retained %d bytes, want at most %d", len(w.head)+len(w.tail), limit)
	}
	if !strings.Contains(got, "bytes omitted") {
		t.Errorf("want an omission marker, got %q", got)
	}
	if !strings.HasPrefix(got, "0123456789") {
		t.Errorf("head lost: %q", got)
	}
	if !strings.HasSuffix(got, "0123456789") {
		t.Errorf("tail lost: %q", got)
	}
}

// Output that fits is returned verbatim, with no marker.
func TestCappedWriterUnderLimitIsExact(t *testing.T) {
	w := &cappedWriter{limit: 1024}
	if _, err := w.Write([]byte("hello world")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := w.String(); got != "hello world" {
		t.Errorf("String() = %q, want %q", got, "hello world")
	}
}

// A file of exactly the read limit is complete, not truncated.
func TestReadFileExactLimitIsNotMarkedTruncated(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir+"/exact.txt", strings.Repeat("a", hardReadLimit))

	out, err := (ReadFile{Root: dir}).Run(context.Background(), `{"path":"exact.txt"}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(out, "file cut off") {
		t.Error("a file of exactly hardReadLimit bytes must not be reported as truncated")
	}
}

// One byte more is truncated, and the note survives the return-value truncation
// because truncate keeps the tail.
func TestReadFileOverLimitIsMarkedTruncated(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir+"/over.txt", strings.Repeat("a", hardReadLimit+1))

	out, err := (ReadFile{Root: dir}).Run(context.Background(), `{"path":"over.txt"}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, "file cut off") {
		t.Error("a file larger than hardReadLimit must be reported as truncated")
	}
}

// Editing loads the whole file, so an oversized file is refused up front rather
// than read into memory.
func TestEditRejectsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir+"/big.txt", strings.Repeat("a", hardReadLimit+1))

	_, err := (Edit{Root: dir}).Run(context.Background(), `{"path":"big.txt","edits":[{"old":"a","new":"b"}]}`)
	if err == nil {
		t.Fatal("expected an error for a file larger than the edit limit")
	}
	if !strings.Contains(err.Error(), "edit limit") {
		t.Errorf("error = %v, want it to mention the edit limit", err)
	}
}

// The edit guard must not reject ordinary files.
func TestEditAllowsNormalFile(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir+"/ok.txt", "hello world")

	if _, err := (Edit{Root: dir}).Run(context.Background(), `{"path":"ok.txt","edits":[{"old":"world","new":"there"}]}`); err != nil {
		t.Fatalf("edit: %v", err)
	}
	got, err := (ReadFile{Root: dir}).Run(context.Background(), `{"path":"ok.txt"}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "hello there" {
		t.Errorf("content = %q, want %q", got, "hello there")
	}
}

// A command's returned output honours MaxOutputBytes regardless of how much the
// process produced.
func TestRunCommandBoundsLargeOutput(t *testing.T) {
	const cap = 512
	out, err := (RunCommand{Dir: t.TempDir(), MaxOutputBytes: cap}).Run(context.Background(), `{"command":"go","args":["env"]}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// truncate keeps half the cap at each end plus a short omission marker.
	if len(out) > cap+128 {
		t.Errorf("returned %d bytes, want the %d-byte cap to apply", len(out), cap)
	}
}
