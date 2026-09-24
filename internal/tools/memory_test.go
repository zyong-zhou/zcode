package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhouyong/zcode/internal/memory"
)

func newMemoryStore(t *testing.T) *memory.Store {
	t.Helper()
	st, err := memory.Open(filepath.Join(t.TempDir(), ".zcode", "memory.md"))
	if err != nil {
		t.Fatalf("memory.Open: %v", err)
	}
	return st
}

func TestRememberTool(t *testing.T) {
	st := newMemoryStore(t)
	ctx := context.Background()
	tool := Remember{Store: st}

	out, err := tool.Run(ctx, `{"text":"项目统一用 gofmt","tags":["约定"]}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "remembered: 项目统一用 gofmt" {
		t.Errorf("out = %q", out)
	}
	if st.Count() != 1 {
		t.Errorf("count = %d, want 1", st.Count())
	}

	if out, err = tool.Run(ctx, `{"text":"项目统一用 gofmt"}`); err != nil || out != "already remembered" {
		t.Errorf("duplicate: out=%q err=%v", out, err)
	}
	if st.Count() != 1 {
		t.Errorf("duplicate should not append: count = %d", st.Count())
	}

	if _, err = tool.Run(ctx, `{"text":"   "}`); err == nil || !strings.Contains(err.Error(), "text is required") {
		t.Errorf("empty text err = %v", err)
	}
	if _, err = tool.Run(ctx, `{`); err == nil || !strings.Contains(err.Error(), "bad arguments") {
		t.Errorf("bad json err = %v", err)
	}
	if _, err = (Remember{}).Run(ctx, `{"text":"x"}`); err == nil || !strings.Contains(err.Error(), "memory is disabled") {
		t.Errorf("nil store err = %v", err)
	}
}

func TestRecallTool(t *testing.T) {
	st := newMemoryStore(t)
	ctx := context.Background()
	for _, args := range []string{
		`{"text":"项目统一用 gofmt","tags":["约定"]}`,
		`{"text":"只支持 Windows"}`,
	} {
		if _, err := (Remember{Store: st}).Run(ctx, args); err != nil {
			t.Fatalf("seed %s: %v", args, err)
		}
	}
	tool := Recall{Store: st}

	out, err := tool.Run(ctx, `{"query":"gofmt"}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "- [约定] 项目统一用 gofmt" {
		t.Errorf("out = %q", out)
	}

	if out, _ = tool.Run(ctx, `{"query":"不存在的词"}`); out != "(no matching memories)" {
		t.Errorf("miss out = %q", out)
	}

	// An empty query matches everything; limit caps the result lines.
	out, _ = tool.Run(ctx, `{"query":"","limit":1}`)
	if lines := strings.Count(out, "\n"); lines != 0 {
		t.Errorf("limit=1 returned %d extra lines: %q", lines, out)
	}
	if _, err = tool.Run(ctx, `{`); err == nil || !strings.Contains(err.Error(), "bad arguments") {
		t.Errorf("bad json err = %v", err)
	}
	if _, err = (Recall{}).Run(ctx, `{"query":"x"}`); err == nil || !strings.Contains(err.Error(), "memory is disabled") {
		t.Errorf("nil store err = %v", err)
	}
}

func TestForgetTool(t *testing.T) {
	st := newMemoryStore(t)
	ctx := context.Background()
	for _, args := range []string{`{"text":"alpha"}`, `{"text":"beta"}`} {
		if _, err := (Remember{Store: st}).Run(ctx, args); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	tool := Forget{Store: st}

	if out, err := tool.Run(ctx, `{"match":"alpha"}`); err != nil || out != "removed 1 memories" {
		t.Errorf("hit: out=%q err=%v", out, err)
	}
	if out, _ := tool.Run(ctx, `{"match":"zzz"}`); out != "removed 0 memories" {
		t.Errorf("miss out = %q", out)
	}
	if _, err := tool.Run(ctx, `{`); err == nil || !strings.Contains(err.Error(), "bad arguments") {
		t.Errorf("bad json err = %v", err)
	}
	if _, err := (Forget{}).Run(ctx, `{"match":"x"}`); err == nil || !strings.Contains(err.Error(), "memory is disabled") {
		t.Errorf("nil store err = %v", err)
	}
}

func TestMemoryToolsArePlanSafe(t *testing.T) {
	st := newMemoryStore(t)
	for _, tool := range []Tool{Remember{Store: st}, Recall{Store: st}, Forget{Store: st}} {
		if !IsPlanSafe(tool) {
			t.Errorf("%s should be plan-safe", tool.Name())
		}
	}
}
