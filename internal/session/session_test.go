package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhouyong/zcode/internal/llm"
)

func TestSessionRoundTrip(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	s, err := store.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	msgs := []llm.Message{
		{Role: "user", Content: "你好"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "1", Name: "echo", Arguments: "{}"}}},
		{Role: "tool", ToolCallID: "1", Content: "ok"},
		{Role: "assistant", Content: "完成"},
	}
	for _, m := range msgs {
		if err := s.Append(m); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != len(msgs) {
		t.Fatalf("loaded %d messages, want %d", len(got), len(msgs))
	}
	if got[1].ToolCalls[0].Name != "echo" || got[3].Content != "完成" {
		t.Errorf("roundtrip mismatch: %+v", got)
	}

	if err := s.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if got, _ := s.Load(); len(got) != 0 {
		t.Errorf("after Reset loaded %d messages, want 0", len(got))
	}
}

func TestSessionToleratesTruncatedLine(t *testing.T) {
	dir := t.TempDir()
	store := Store{Dir: dir}
	s, err := store.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = s.Append(llm.Message{Role: "user", Content: "kept"})
	path := s.Path()
	_ = s.Close()

	// Simulate an interrupted write: a valid line followed by a partial object.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"role":"assistant","content":"cut`)
	_ = f.Close()

	got, err := (&Session{path: path}).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 || got[0].Content != "kept" {
		t.Errorf("got %+v, want the single valid message", got)
	}
}

func TestOpenErrors(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if _, err := store.Open("nope"); err == nil {
		t.Error("opening a missing session should fail")
	}
	if _, err := store.Open("../escape"); err == nil {
		t.Error("path traversal should fail")
	}
	if _, err := store.Open(""); err == nil {
		t.Error("empty id should fail")
	}
}

func TestListAndLatest(t *testing.T) {
	store := Store{Dir: t.TempDir()}

	// Empty store: no sessions, no error.
	if infos, err := store.List(); err != nil || len(infos) != 0 {
		t.Fatalf("empty List = %v, %v", infos, err)
	}
	if id, err := store.Latest(); err != nil || id != "" {
		t.Fatalf("empty Latest = %q, %v", id, err)
	}

	first, err := store.New()
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Append(llm.Message{Role: "user", Content: "第一个会话"})
	_ = first.Close()
	time.Sleep(10 * time.Millisecond)

	second, err := store.New()
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Append(llm.Message{Role: "user", Content: "第二个会话"})
	_ = second.Close()

	infos, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("List returned %d, want 2", len(infos))
	}
	if infos[0].ID != second.ID() {
		t.Errorf("newest first: got %q, want %q", infos[0].ID, second.ID())
	}
	if !strings.Contains(infos[0].Preview, "第二个会话") {
		t.Errorf("preview = %q", infos[0].Preview)
	}
	if id, _ := store.Latest(); id != second.ID() {
		t.Errorf("Latest = %q, want %q", id, second.ID())
	}
}

func TestManagerRotationAndTranscript(t *testing.T) {
	m := NewManager(Store{Dir: t.TempDir()})
	defer m.Close()

	id1, err := m.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := m.Append(llm.Message{Role: "user", Content: "一"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if got, _, n := m.Current(); got != id1 || n != 1 {
		t.Errorf("Current = %q/%d, want %q/1", got, n, id1)
	}

	id2, err := m.Open(id1)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if id2 != id1 {
		t.Errorf("Open returned %q, want %q", id2, id1)
	}
	msgs, err := m.Load()
	if err != nil || len(msgs) != 1 {
		t.Fatalf("transcript Load = %+v, %v", msgs, err)
	}

	if _, err := m.Open("missing"); err == nil {
		t.Error("opening a missing session should fail")
	}
	if _, err := m.New(); err != nil {
		t.Fatalf("second New: %v", err)
	}
	if msgs, _ := m.Load(); len(msgs) != 0 {
		t.Errorf("after rotation transcript should be empty, got %d", len(msgs))
	}
}

func TestSessionDirCreated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "sessions")
	store := Store{Dir: dir}
	s, err := store.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("session dir not created: %v", err)
	}
}
