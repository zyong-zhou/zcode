// Package compact shrinks a conversation history that is approaching the model
// context window. It works in two stages: first it prunes bulky old tool
// results, then it collapses the oldest turns into a single summary produced by
// a caller-supplied summarizer (usually an LLM call).
package compact

import (
	"context"
	"fmt"
	"strings"

	"github.com/zyong-zhou/zcode/internal/llm"
)

// placeholder replaces a pruned tool result.
const placeholder = "[较早的工具输出已省略以节省上下文]"

// Options configures compaction.
type Options struct {
	// MaxTokens is the model context window. Zero disables compaction.
	MaxTokens int
	// TriggerAt is the fraction of MaxTokens at which compaction runs.
	TriggerAt float64
	// KeepRecent is the number of recent messages always kept verbatim.
	KeepRecent int
}

// Summarizer turns a plain-text transcript into a short summary.
type Summarizer func(ctx context.Context, text string) (string, error)

// Compactor implements zcode.Compactor.
type Compactor struct {
	opts Options
	sum  Summarizer
}

// New returns a compactor. A nil summarizer disables the summarization stage;
// pruning still applies.
func New(opts Options, sum Summarizer) *Compactor {
	if opts.TriggerAt <= 0 || opts.TriggerAt >= 1 {
		opts.TriggerAt = 0.75
	}
	if opts.KeepRecent < 1 {
		opts.KeepRecent = 6
	}
	return &Compactor{opts: opts, sum: sum}
}

// Compact returns the messages to send. When the history is under the trigger
// it is returned unchanged with changed=false. On a summarizer failure it
// returns the best-effort (pruned) history together with the error.
func (c *Compactor) Compact(ctx context.Context, msgs []llm.Message) ([]llm.Message, bool, error) {
	if c == nil || c.opts.MaxTokens <= 0 {
		return msgs, false, nil
	}
	trigger := int(float64(c.opts.MaxTokens) * c.opts.TriggerAt)
	if EstimateTokens(msgs) < trigger {
		return msgs, false, nil
	}

	// Stage 1: drop bulky old tool results.
	keepTool := c.opts.KeepRecent / 2
	if keepTool < 1 {
		keepTool = 1
	}
	pruned, prunedChanged := Prune(msgs, keepTool)
	if prunedChanged && EstimateTokens(pruned) < trigger {
		return pruned, true, nil
	}

	// Stage 2: summarize the oldest turns.
	out, summarized, err := Summarize(ctx, pruned, c.opts.KeepRecent, c.sum)
	if err != nil {
		return pruned, prunedChanged, err
	}
	return out, prunedChanged || summarized, nil
}

// EstimateTokens approximates the prompt size. It is deliberately rough: the
// goal is to trigger compaction before the hard limit, not to bill tokens.
// CJK runes count as one token each; other bytes as roughly a quarter.
func EstimateTokens(msgs []llm.Message) int {
	total := 0
	for _, m := range msgs {
		total += 4 // per-message overhead
		total += estimateText(m.Content)
		for _, tc := range m.ToolCalls {
			total += 4 + estimateText(tc.Name) + estimateText(tc.Arguments)
		}
	}
	return total
}

func estimateText(s string) int {
	runes := []rune(s)
	cjk := 0
	for _, r := range runes {
		if r > 0x2E80 {
			cjk++
		}
	}
	ascii := len(runes) - cjk
	return cjk + (ascii+3)/4
}

// Prune replaces the content of older tool messages with a placeholder, keeping
// the most recent keep results. Assistant tool_calls are left in place so the
// tool messages remain valid.
func Prune(msgs []llm.Message, keep int) ([]llm.Message, bool) {
	if keep < 0 {
		keep = 0
	}
	var idx []int
	for i, m := range msgs {
		if m.Role == "tool" {
			idx = append(idx, i)
		}
	}
	if len(idx) <= keep {
		return msgs, false
	}
	out := append([]llm.Message(nil), msgs...)
	changed := false
	for _, i := range idx[:len(idx)-keep] {
		if out[i].Content == placeholder {
			continue
		}
		out[i] = llm.Message{Role: "tool", ToolCallID: out[i].ToolCallID, Content: placeholder}
		changed = true
	}
	return out, changed
}

// Summarize replaces all but the last keep messages with a single summary
// message placed right after the system message. It never splits a tool-call
// group. A nil summarizer is a no-op.
func Summarize(ctx context.Context, msgs []llm.Message, keep int, sum Summarizer) ([]llm.Message, bool, error) {
	if sum == nil || keep < 0 {
		return msgs, false, nil
	}
	start := 0
	if len(msgs) > 0 && msgs[0].Role == "system" {
		start = 1
	}
	cut := len(msgs) - keep
	// Keep a tool result with its requesting assistant message.
	for cut > start && cut < len(msgs) && msgs[cut].Role == "tool" {
		cut--
	}
	if cut <= start {
		return msgs, false, nil
	}

	summary, err := sum(ctx, render(msgs[start:cut]))
	if err != nil {
		return msgs, false, err
	}
	out := make([]llm.Message, 0, start+1+len(msgs)-cut)
	out = append(out, msgs[:start]...)
	out = append(out, llm.Message{Role: "user", Content: "[早期对话摘要]\n" + strings.TrimSpace(summary)})
	out = append(out, msgs[cut:]...)
	return out, true, nil
}

// render flattens messages into plain text for the summarizer.
func render(msgs []llm.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case "user":
			fmt.Fprintf(&b, "用户：%s\n", m.Content)
		case "assistant":
			if strings.TrimSpace(m.Content) != "" {
				fmt.Fprintf(&b, "助手：%s\n", m.Content)
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&b, "助手调用工具 %s(%s)\n", tc.Name, tc.Arguments)
			}
		case "tool":
			fmt.Fprintf(&b, "工具结果：%s\n", m.Content)
		case "system":
			fmt.Fprintf(&b, "系统：%s\n", m.Content)
		}
	}
	return b.String()
}
