package compact

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zyong-zhou/zcode/internal/llm"
)

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens(nil); got != 0 {
		t.Errorf("empty = %d, want 0", got)
	}
	short := EstimateTokens([]llm.Message{{Role: "user", Content: "hi"}})
	long := EstimateTokens([]llm.Message{{Role: "user", Content: strings.Repeat("a", 400)}})
	if long <= short {
		t.Errorf("long = %d should exceed short = %d", long, short)
	}
	// CJK counts heavier than the same number of ASCII runes.
	cjk := EstimateTokens([]llm.Message{{Role: "user", Content: "汉字"}})
	ascii := EstimateTokens([]llm.Message{{Role: "user", Content: "ab"}})
	if cjk <= ascii {
		t.Errorf("CJK = %d should exceed ASCII = %d", cjk, ascii)
	}
}

func TestPrune(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "s"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "1", Name: "a"}}},
		{Role: "tool", ToolCallID: "1", Content: "old and big"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "2", Name: "b"}}},
		{Role: "tool", ToolCallID: "2", Content: "recent"},
	}
	out, changed := Prune(msgs, 1)
	if !changed {
		t.Fatal("expected a change")
	}
	if out[2].Content != placeholder || out[2].ToolCallID != "1" {
		t.Errorf("old tool message = %+v", out[2])
	}
	if out[4].Content != "recent" {
		t.Errorf("recent tool message was pruned: %+v", out[4])
	}
	if _, changed := Prune(msgs, 5); changed {
		t.Error("prune with keep >= count should be a no-op")
	}
}

func TestSummarize(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "u2"},
		{Role: "assistant", Content: "a2"},
	}
	var input string
	sum := func(_ context.Context, text string) (string, error) { input = text; return "SUMMARY", nil }
	out, ok, err := Summarize(context.Background(), msgs, 2, sum)
	if err != nil || !ok {
		t.Fatalf("Summarize: ok=%v err=%v", ok, err)
	}
	if len(out) != 4 {
		t.Fatalf("len(out) = %d, want 4 (system + summary + last 2)", len(out))
	}
	if out[0].Role != "system" || !strings.Contains(out[1].Content, "SUMMARY") {
		t.Errorf("out = %+v", out)
	}
	if !strings.Contains(input, "u1") || strings.Contains(input, "u2") {
		t.Errorf("summarizer input = %q", input)
	}
}

func TestSummarizeKeepsToolGroupTogether(t *testing.T) {
	msgs := []llm.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "1", Name: "t"}}},
		{Role: "tool", ToolCallID: "1", Content: "r1"},
		{Role: "assistant", Content: "a"},
	}
	// keep=2 would cut at the tool message; the cut must move back so the
	// assistant tool_calls and its tool result stay together.
	out, ok, err := Summarize(context.Background(), msgs, 2, func(context.Context, string) (string, error) {
		return "S", nil
	})
	if err != nil || !ok {
		t.Fatalf("Summarize: ok=%v err=%v", ok, err)
	}
	if out[1].Role != "assistant" || out[2].Role != "tool" {
		t.Errorf("tool group was split: %+v", out)
	}
}

func TestSummarizeNilSummarizer(t *testing.T) {
	if _, ok, err := Summarize(context.Background(), []llm.Message{{Role: "user"}}, 0, nil); ok || err != nil {
		t.Errorf("nil summarizer: ok=%v err=%v, want no-op", ok, err)
	}
}

func TestCompactor(t *testing.T) {
	big := strings.Repeat("x", 4000)
	msgs := []llm.Message{
		{Role: "system", Content: "s"},
		{Role: "user", Content: "first"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "1", Name: "t"}}},
		{Role: "tool", ToolCallID: "1", Content: big},
		{Role: "assistant", Content: "early"},
		{Role: "user", Content: "second"},
		{Role: "assistant", Content: "late"},
	}

	// Under the trigger: untouched.
	under := New(Options{MaxTokens: 1_000_000, TriggerAt: 0.75, KeepRecent: 2}, nil)
	if _, changed, err := under.Compact(context.Background(), msgs); changed || err != nil {
		t.Errorf("under trigger: changed=%v err=%v", changed, err)
	}

	// Over the trigger: the oldest turns collapse into a summary.
	over := New(Options{MaxTokens: 100, TriggerAt: 0.5, KeepRecent: 2}, func(context.Context, string) (string, error) {
		return "摘要", nil
	})
	out, changed, err := over.Compact(context.Background(), msgs)
	if err != nil || !changed {
		t.Fatalf("over trigger: changed=%v err=%v", changed, err)
	}
	if len(out) >= len(msgs) || !strings.Contains(out[1].Content, "摘要") {
		t.Errorf("compacted = %+v", out)
	}

	// A failing summarizer keeps the pruned history and reports the error.
	failing := New(Options{MaxTokens: 100, TriggerAt: 0.5, KeepRecent: 2}, func(context.Context, string) (string, error) {
		return "", errors.New("boom")
	})
	if _, _, err := failing.Compact(context.Background(), msgs); err == nil {
		t.Error("expected the summarizer error to surface")
	}

	// Zero MaxTokens disables compaction entirely.
	off := New(Options{}, nil)
	if _, changed, err := off.Compact(context.Background(), msgs); changed || err != nil {
		t.Errorf("disabled: changed=%v err=%v", changed, err)
	}
}
