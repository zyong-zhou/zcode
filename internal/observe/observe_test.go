package observe

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zyong-zhou/zcode/internal/llm"
	"github.com/zyong-zhou/zcode/internal/zcode"
)

func TestTextObserver(t *testing.T) {
	var buf bytes.Buffer
	obs := Text(&buf)
	obs(zcode.Event{Step: 1, Kind: zcode.EventAssistant, Content: "thinking", Usage: llm.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}})
	obs(zcode.Event{Step: 1, Kind: zcode.EventThought, Content: "reasoning", Usage: llm.Usage{PromptTokens: 11, CompletionTokens: 3, TotalTokens: 14}})
	obs(zcode.Event{Step: 1, Kind: zcode.EventToolCall, Tool: "read_file", Args: `{"path":"a.go"}`})
	obs(zcode.Event{Step: 1, Kind: zcode.EventToolResult, Tool: "read_file", Result: "line1\nline2", DurationMS: 5})
	obs(zcode.Event{Step: 2, Kind: zcode.EventFinal, Content: "done"})

	out := buf.String()
	for _, want := range []string{"assistant in", "thought in", "tokens(p/c/t)=10/2/12", "tokens(p/c/t)=11/3/14", "-> read_file", "<- read_file", "final answer"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "reasoning") {
		t.Errorf("thought content missing:\n%s", out)
	}
	if strings.Contains(out, "line1\nline2") {
		t.Errorf("multi-line result should be flattened:\n%s", out)
	}
}

func TestTraceObserver(t *testing.T) {
	var buf bytes.Buffer
	NewTrace(&buf).Observe(zcode.Event{Step: 1, Kind: zcode.EventToolCall, Tool: "echo", Args: "{}"})

	var got zcode.Event
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatalf("decode trace line: %v", err)
	}
	if got.Kind != zcode.EventToolCall || got.Tool != "echo" || got.Step != 1 {
		t.Errorf("decoded event = %+v", got)
	}
}

func TestOneLine(t *testing.T) {
	if got := oneLine("a\n\n  b\tc", 0); got != "a b c" {
		t.Errorf("flatten = %q, want %q", got, "a b c")
	}
	if got := oneLine(strings.Repeat("你", 10), 3); got != "你你你..." {
		t.Errorf("truncate = %q", got)
	}
}

func TestTextObserverStatusEvents(t *testing.T) {
	var buf bytes.Buffer
	obs := Text(&buf)
	// An errored tool result with blank output must not print a body line.
	obs(zcode.Event{Step: 3, Kind: zcode.EventToolResult, Tool: "run_command", Err: "boom", Result: "   "})
	obs(zcode.Event{Step: 3, Kind: zcode.EventPermission, Tool: "write_file", Result: "allow"})
	obs(zcode.Event{Step: 3, Kind: zcode.EventCompact, Result: "压缩完成"})
	obs(zcode.Event{Step: 3, Kind: zcode.EventWarning, Err: "警告内容"})
	obs(zcode.Event{Step: 4, Kind: zcode.EventLimit, Err: "达到上限"})

	out := buf.String()
	for _, want := range []string{
		"error: boom",
		"permission write_file: allow",
		"压缩完成",
		"warning: 警告内容",
		"stopped: 达到上限",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}
