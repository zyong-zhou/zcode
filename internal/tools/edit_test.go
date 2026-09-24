package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEditTool(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	mustWrite(t, path, "hello world\nhello again\n")
	ctx := context.Background()

	out, err := (Edit{Root: root}).Run(ctx, `{"path":"a.txt","edits":[{"old":"world","new":"there"}]}`)
	if err != nil || !strings.Contains(out, "edited a.txt") {
		t.Fatalf("unique replace: out=%q err=%v", out, err)
	}
	if got := readFile(t, path); got != "hello there\nhello again\n" {
		t.Errorf("after replace = %q", got)
	}

	if _, err := (Edit{Root: root}).Run(ctx, `{"path":"a.txt","edits":[{"old":"hello","new":"hi"}]}`); err == nil || !strings.Contains(err.Error(), "occurs 2 times") {
		t.Errorf("ambiguous err = %v", err)
	}

	if _, err := (Edit{Root: root}).Run(ctx, `{"path":"a.txt","edits":[{"old":"hello","new":"hi","replace_all":true}]}`); err != nil {
		t.Fatalf("replace_all: %v", err)
	}
	if got := readFile(t, path); got != "hi there\nhi again\n" {
		t.Errorf("after replace_all = %q", got)
	}

	if _, err := (Edit{Root: root}).Run(ctx, `{"path":"a.txt","edits":[{"old":"zzz","new":"x"}]}`); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("not-found err = %v", err)
	}
}

func TestEditToolValidationAndEscape(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.txt"), "x")
	ctx := context.Background()

	if _, err := (Edit{Root: root}).Run(ctx, `{`); err == nil || !strings.Contains(err.Error(), "bad arguments") {
		t.Errorf("bad json err = %v", err)
	}
	if _, err := (Edit{Root: root}).Run(ctx, `{"path":"a.txt"}`); err == nil || !strings.Contains(err.Error(), "at least one edit") {
		t.Errorf("empty edits err = %v", err)
	}
	if _, err := (Edit{Root: root}).Run(ctx, `{"path":"a.txt","edits":[]}`); err == nil || !strings.Contains(err.Error(), "at least one edit") {
		t.Errorf("empty edits list err = %v", err)
	}
	if _, err := (Edit{Root: root}).Run(ctx, `{"path":"a.txt","edits":[{"old":"","new":"y"}]}`); err == nil || !strings.Contains(err.Error(), "old is required") {
		t.Errorf("empty old err = %v", err)
	}
	if _, err := (Edit{Root: root}).Run(ctx, `{"path":"../escape.txt","edits":[{"old":"a","new":"b"}]}`); err == nil || !strings.Contains(err.Error(), "escapes project root") {
		t.Errorf("escape err = %v", err)
	}
	if _, err := (Edit{Root: root}).Run(ctx, `{"path":"missing.txt","edits":[{"old":"a","new":"b"}]}`); err == nil {
		t.Errorf("editing a missing file should fail, got %v", err)
	}
	if IsPlanSafe(Edit{Root: root}) {
		t.Error("edit must not be plan-safe")
	}
}
