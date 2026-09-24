// Package observe provides event sinks for the agent: a human-readable progress
// stream and a JSONL trace for replay.
package observe

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/zhouyong/zcode/internal/zcode"
)

// Text returns an observer that writes one line per event to w.
func Text(w io.Writer) func(zcode.Event) {
	return func(e zcode.Event) {
		switch e.Kind {
		case zcode.EventAssistant:
			writeReply(w, "assistant", e)
		case zcode.EventThought:
			writeReply(w, "thought", e)
		case zcode.EventToolCall:
			fmt.Fprintf(w, "[step %d] -> %s %s\n", e.Step, e.Tool, oneLine(e.Args, 200))
		case zcode.EventToolResult:
			status := "ok"
			if e.Err != "" {
				status = "error: " + e.Err
			}
			fmt.Fprintf(w, "[step %d] <- %s (%s, %d chars, %s)\n",
				e.Step, e.Tool, status, len(e.Result), fmtDur(e.DurationMS))
			if p := oneLine(e.Result, 200); p != "" {
				fmt.Fprintf(w, "          %s\n", p)
			}
		case zcode.EventFinal:
			fmt.Fprintf(w, "[step %d] final answer (%d chars)\n", e.Step, len(e.Content))
		case zcode.EventPermission:
			fmt.Fprintf(w, "[step %d] permission %s: %s\n", e.Step, e.Tool, e.Result)
		case zcode.EventCompact:
			fmt.Fprintf(w, "[step %d] %s\n", e.Step, e.Result)
		case zcode.EventWarning:
			fmt.Fprintf(w, "[step %d] warning: %s\n", e.Step, e.Err)
		case zcode.EventLimit:
			fmt.Fprintf(w, "[step %d] stopped: %s\n", e.Step, e.Err)
		}
	}
}

// writeReply prints an assistant answer or a reasoning thought: a header with
// timing and token usage, then the flattened text.
func writeReply(w io.Writer, label string, e zcode.Event) {
	fmt.Fprintf(w, "[step %d] %s in %s", e.Step, label, fmtDur(e.DurationMS))
	if !e.Usage.IsZero() {
		fmt.Fprintf(w, " tokens(p/c/t)=%d/%d/%d",
			e.Usage.PromptTokens, e.Usage.CompletionTokens, e.Usage.TotalTokens)
	}
	fmt.Fprintln(w)
	if p := oneLine(e.Content, 200); p != "" {
		fmt.Fprintf(w, "          %s\n", p)
	}
}

// Trace writes one JSON object per event, suitable for replay or analysis.
type Trace struct {
	mu  sync.Mutex
	enc *json.Encoder
}

// NewTrace returns a Trace that writes newline-delimited JSON to w.
func NewTrace(w io.Writer) *Trace {
	return &Trace{enc: json.NewEncoder(w)}
}

// Observe implements the observer signature expected by zcode.Agent.OnEvent.
func (t *Trace) Observe(e zcode.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	_ = t.enc.Encode(e)
}

func fmtDur(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).String()
}

// oneLine flattens whitespace and truncates to max runes (rune-safe).
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if max <= 0 {
		return s
	}
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "..."
	}
	return s
}
