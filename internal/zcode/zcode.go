// Package zcode implements the reasoning loop that turns a user request into a
// program design or code change by repeatedly calling the model and, when
// asked, running tools. The loop is a ReAct cycle: reason, act, observe,
// repeat until the model answers without requesting a tool.
package zcode

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zyong-zhou/zcode/internal/llm"
	"github.com/zyong-zhou/zcode/internal/tools"
)

const (
	defaultMaxSteps    = 12
	defaultRepeatLimit = 3
	// maxRepeatNudges bounds how many times a blocked repeated call is tolerated
	// before the run is aborted.
	maxRepeatNudges = 3
	// maxMemoryPromptRunes caps how much memory text is injected.
	maxMemoryPromptRunes = 2700
	// maxRulesPromptRunes caps how much rule text is injected.
	maxRulesPromptRunes = 6000
)

// EventKind classifies an observability event emitted during a run.
type EventKind string

const (
	// EventAssistant is emitted after a model reply that is the final answer.
	EventAssistant EventKind = "assistant"
	// EventThought is emitted after a model reply that only requests tools.
	EventThought EventKind = "thought"
	// EventToolCall is emitted before a tool runs.
	EventToolCall EventKind = "tool_call"
	// EventToolResult is emitted after a tool returns.
	EventToolResult EventKind = "tool_result"
	// EventFinal is emitted when the model answers without requesting a tool.
	EventFinal EventKind = "final"
	// EventLimit is emitted when MaxSteps, the timeout, or the repeat guard
	// stops the run.
	EventLimit EventKind = "limit"
	// EventPermission is emitted when a tool call is gated by policy.
	EventPermission EventKind = "permission"
	// EventWarning is emitted for a non-fatal side-channel failure such as a
	// transcript write error; the run continues.
	EventWarning EventKind = "warning"
	// EventCompact is emitted when the history is compacted to fit the window.
	EventCompact EventKind = "compact"
)

// Event describes one observable step of a run.
type Event struct {
	At         time.Time `json:"at"`
	Step       int       `json:"step"`
	Kind       EventKind `json:"kind"`
	Mode       Mode      `json:"mode,omitempty"`
	Tool       string    `json:"tool,omitempty"`
	Args       string    `json:"args,omitempty"`
	Result     string    `json:"result,omitempty"`
	Content    string    `json:"content,omitempty"`
	Err        string    `json:"error,omitempty"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	Usage      llm.Usage `json:"usage,omitempty"`
}

// MemoryProvider supplies project memory text that is injected into the system
// prompt at the start of every run. It may be nil.
type MemoryProvider interface {
	Render() string
}

// PermissionRequest describes a tool call awaiting an approval decision.
type PermissionRequest struct {
	Tool string
	Args string
	// Preview is a short human-readable summary of what the call will do, when
	// the tool can describe it (for example a file write). It may be empty.
	Preview string
}

// PermissionFunc returns the policy decision for a tool call: "allow", "ask",
// or "deny". A nil func allows everything.
type PermissionFunc func(toolName, args string) string

// OnPermission resolves an "ask" decision and reports whether the call may run.
// When it is nil, "ask" is treated as "deny" so non-interactive runs stay safe.
type OnPermission func(PermissionRequest) bool

// Transcript persists and restores conversation messages so a session can be
// resumed across runs. Implementations must be safe to call while a run is in
// progress. It may be nil.
type Transcript interface {
	// Load returns previously recorded messages, oldest first.
	Load() ([]llm.Message, error)
	// Append records one message.
	Append(llm.Message) error
	// Reset discards all recorded messages.
	Reset() error
}

// Compactor shrinks the message history when it approaches the model context
// limit. Compact returns the messages to send, whether it changed them, and any
// error (which is non-fatal: the best-effort result is still used).
type Compactor interface {
	Compact(ctx context.Context, msgs []llm.Message) ([]llm.Message, bool, error)
}

// Options configures a new Agent. Zero values fall back to sane defaults.
type Options struct {
	// System is the base (build) system prompt.
	System string
	// PlanSystem is the system prompt used in plan mode. When empty, System is
	// used instead.
	PlanSystem string
	// Mode is the initial mode; defaults to ModeBuild.
	Mode Mode
	// MaxSteps bounds the number of model turns per run.
	MaxSteps int
	// Timeout bounds the whole run; zero disables it.
	Timeout time.Duration
	// RepeatLimit is how many identical (tool, args) calls are allowed before
	// the call is blocked. Defaults to 3.
	RepeatLimit int
	// Memory, when set, is injected into the system prompt.
	Memory MemoryProvider
	// Rules, when set, is injected into the system prompt before memory.
	Rules MemoryProvider
	// Permissions, when set, gates every tool call.
	Permissions PermissionFunc
	// OnPermission resolves "ask" decisions. Nil means ask is denied.
	OnPermission OnPermission
	// Transcript, when set, records the conversation so it can be resumed.
	Transcript Transcript
	// Compactor, when set, shrinks the history before each request.
	Compactor Compactor
}

// Agent drives a model through a task. It is not safe for concurrent use.
type Agent struct {
	Client      llm.Client
	Tools       *tools.Registry
	System      string
	PlanSystem  string
	Mode        Mode
	MaxSteps    int
	Timeout     time.Duration
	RepeatLimit int
	Memory      MemoryProvider
	Rules       MemoryProvider
	// Permissions, when set, gates every tool call. OnPermission resolves "ask".
	Permissions  PermissionFunc
	OnPermission OnPermission
	// Transcript, when set, records and restores the conversation.
	Transcript Transcript
	// Compactor, when set, shrinks the history before each request.
	Compactor Compactor
	// OnEvent, when set, receives one Event per observable step.
	OnEvent func(Event)
	// OnDelta, when set, receives streamed assistant text fragments. It is only
	// used when the underlying client implements llm.StreamClient.
	OnDelta func(string)
	// Usage accumulates token usage across all calls since the last Design.
	Usage llm.Usage

	// history carries the conversation across Ask calls so follow-ups keep
	// context. Design resets it.
	history []llm.Message
}

// New builds an Agent with sane defaults.
func New(client llm.Client, reg *tools.Registry, opts Options) *Agent {
	mode := opts.Mode
	if !mode.Valid() {
		mode = ModeBuild
	}
	return &Agent{
		Client:       client,
		Tools:        reg,
		System:       opts.System,
		PlanSystem:   opts.PlanSystem,
		Mode:         mode,
		MaxSteps:     opts.MaxSteps,
		Timeout:      opts.Timeout,
		RepeatLimit:  opts.RepeatLimit,
		Memory:       opts.Memory,
		Rules:        opts.Rules,
		Permissions:  opts.Permissions,
		OnPermission: opts.OnPermission,
		Transcript:   opts.Transcript,
		Compactor:    opts.Compactor,
	}
}

// SetMode switches the active mode, ignoring unknown values. The conversation
// history is preserved so build mode can act on a plan.
func (a *Agent) SetMode(m Mode) {
	if m.Valid() {
		a.Mode = m
	}
}

// Design runs a one-shot request, resetting any prior conversation and usage.
func (a *Agent) Design(ctx context.Context, request string) (string, error) {
	if a.Client == nil {
		return "", fmt.Errorf("zcode: nil llm client")
	}
	a.Usage = llm.Usage{}
	a.history = nil

	msgs := a.seed()
	msgs = a.appendMessage(msgs, llm.Message{Role: "user", Content: request})
	return a.run(ctx, msgs)
}

// Ask continues the conversation, preserving prior turns so follow-up requests
// keep context. Token usage accumulates across calls; use Reset to start over.
func (a *Agent) Ask(ctx context.Context, input string) (string, error) {
	if a.Client == nil {
		return "", fmt.Errorf("zcode: nil llm client")
	}
	if a.history == nil {
		a.history = a.seed()
	}
	a.history = a.appendMessage(a.history, llm.Message{Role: "user", Content: input})
	return a.run(ctx, a.history)
}

// Reset clears the conversation history and any persisted transcript, but
// leaves accumulated Usage intact.
func (a *Agent) Reset() {
	a.history = nil
	if a.Transcript != nil {
		if err := a.Transcript.Reset(); err != nil {
			a.emit(Event{Kind: EventWarning, Err: "transcript: " + err.Error()})
		}
	}
}

// Restore loads a persisted transcript into history so a session can be
// resumed. The system message is regenerated on the next run. It is a no-op
// when no Transcript is configured.
func (a *Agent) Restore() error {
	if a.Transcript == nil {
		return nil
	}
	msgs, err := a.Transcript.Load()
	if err != nil {
		return fmt.Errorf("zcode: restore: %w", err)
	}
	a.history = msgs
	return nil
}

// History returns a copy of the current conversation so a caller can inspect or
// estimate its size without mutating the agent.
func (a *Agent) History() []llm.Message {
	out := make([]llm.Message, len(a.history))
	copy(out, a.history)
	return out
}

// Compact runs the configured Compactor once against the current history and
// replaces it when anything changed. It reports whether the history shrank. It
// is a no-op when no Compactor is configured.
func (a *Agent) Compact(ctx context.Context) (bool, error) {
	if a.Compactor == nil || len(a.history) == 0 {
		return false, nil
	}
	next, changed, err := a.Compactor.Compact(ctx, a.history)
	if changed {
		a.history = next
	}
	return changed, err
}

// appendMessage adds one message to history and mirrors it to the transcript.
func (a *Agent) appendMessage(msgs []llm.Message, m llm.Message) []llm.Message {
	msgs = append(msgs, m)
	a.history = msgs
	if a.Transcript != nil {
		if err := a.Transcript.Append(m); err != nil {
			a.emit(Event{Kind: EventWarning, Err: "transcript: " + err.Error()})
		}
	}
	return msgs
}

// prompt returns the base system prompt for the active mode.
func (a *Agent) prompt() string {
	if a.Mode == ModePlan && a.PlanSystem != "" {
		return a.PlanSystem
	}
	return a.System
}

// systemMessage composes the system prompt with project rules and memory.
func (a *Agent) systemMessage() llm.Message {
	base := strings.TrimSpace(a.prompt())
	if a.Rules != nil {
		if r := strings.TrimSpace(a.Rules.Render()); r != "" {
			if base != "" {
				base += "\n\n"
			}
			base += "# 项目规则（自动加载）\n" + excerptRunes(r, maxRulesPromptRunes)
		}
	}
	if a.Memory != nil {
		if mem := strings.TrimSpace(a.Memory.Render()); mem != "" {
			if base != "" {
				base += "\n\n"
			}
			base += "# 项目记忆（自动加载）\n" + excerptRunes(mem, maxMemoryPromptRunes)
		}
	}
	return llm.Message{Role: "system", Content: base}
}

func (a *Agent) seed() []llm.Message {
	sys := a.systemMessage()
	if strings.TrimSpace(sys.Content) == "" {
		return nil
	}
	return []llm.Message{sys}
}

// refreshSystem rewrites (or prepends) the system message so mode changes and
// newly remembered facts take effect on the next turn without dropping history.
func (a *Agent) refreshSystem(msgs []llm.Message) []llm.Message {
	sys := a.systemMessage()
	if strings.TrimSpace(sys.Content) == "" {
		return msgs
	}
	if len(msgs) > 0 && msgs[0].Role == "system" {
		msgs[0] = sys
		return msgs
	}
	return append([]llm.Message{sys}, msgs...)
}

// allowed returns the tool under name if it is available in the active mode.
// In plan mode, tools that are not plan-safe are reported as unknown.
func (a *Agent) allowed(name string) (tools.Tool, bool) {
	if a.Tools == nil {
		return nil, false
	}
	t, ok := a.Tools.Get(name)
	if !ok {
		return nil, false
	}
	if a.Mode == ModePlan && !tools.IsPlanSafe(t) {
		return nil, false
	}
	return t, true
}

// checkPermission applies the permission policy to a tool call. It returns the
// raw decision and whether the call may run. An "ask" with no OnPermission
// handler is denied, so non-interactive runs cannot silently proceed.
func (a *Agent) checkPermission(call llm.ToolCall) (string, bool) {
	decision := "allow"
	if a.Permissions != nil {
		decision = strings.ToLower(strings.TrimSpace(a.Permissions(call.Name, call.Arguments)))
	}
	switch decision {
	case "", "allow":
		return "allow", true
	case "deny":
		return "deny", false
	case "ask":
		if a.OnPermission != nil && a.OnPermission(PermissionRequest{Tool: call.Name, Args: call.Arguments, Preview: a.previewFor(call)}) {
			return "ask", true
		}
		return "ask", false
	default:
		// Unknown decisions are denied rather than silently allowed.
		return decision, false
	}
}

// previewFor returns the tool's own description of a pending call, or "".
func (a *Agent) previewFor(call llm.ToolCall) string {
	if a.Tools == nil {
		return ""
	}
	t, ok := a.Tools.Get(call.Name)
	if !ok {
		return ""
	}
	return tools.Preview(t, call.Arguments)
}

// run executes the reasoning loop over msgs, keeping a.history in sync so
// multi-turn callers retain context, and returns the final answer.
func (a *Agent) run(ctx context.Context, msgs []llm.Message) (string, error) {
	msgs = a.refreshSystem(msgs)
	a.history = msgs

	maxSteps := a.MaxSteps
	if maxSteps <= 0 {
		maxSteps = defaultMaxSteps
	}
	repeatLimit := a.RepeatLimit
	if repeatLimit <= 0 {
		repeatLimit = defaultRepeatLimit
	}

	if a.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.Timeout)
		defer cancel()
	}

	seen := map[string]int{}
	nudges := 0

	for step := 1; step <= maxSteps; step++ {
		start := time.Now()

		// Compact the history before it overflows the model's context window.
		if a.Compactor != nil {
			compacted, changed, cerr := a.Compactor.Compact(ctx, msgs)
			if changed {
				msgs = compacted
				a.history = msgs
				a.emit(Event{
					Step:   step,
					Kind:   EventCompact,
					Result: fmt.Sprintf("history compacted to %d messages", len(msgs)),
				})
			}
			if cerr != nil {
				a.emit(Event{Step: step, Kind: EventWarning, Err: "compact: " + cerr.Error()})
			}
		}

		req := llm.Request{Messages: msgs, Tools: a.toolSpecs()}
		var resp llm.Response
		var err error
		if sc, ok := a.Client.(llm.StreamClient); ok && a.OnDelta != nil {
			resp, err = sc.ChatStream(ctx, req, a.OnDelta)
		} else {
			resp, err = a.Client.Chat(ctx, req)
		}
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded {
				msg := fmt.Sprintf("zcode: timed out after %s", a.Timeout)
				a.emit(Event{Step: step, Kind: EventLimit, Err: msg})
				return "", fmt.Errorf("%s", msg)
			}
			return "", fmt.Errorf("zcode: chat: %w", err)
		}
		a.Usage = a.Usage.Add(resp.Usage)

		kind := EventAssistant
		if len(resp.ToolCalls) > 0 {
			kind = EventThought
		}
		a.emit(Event{
			Step:       step,
			Kind:       kind,
			Content:    resp.Content,
			DurationMS: time.Since(start).Milliseconds(),
			Usage:      resp.Usage,
		})

		msgs = a.appendMessage(msgs, llm.Message{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})
		if len(resp.ToolCalls) == 0 {
			a.emit(Event{Step: step, Kind: EventFinal, Content: resp.Content})
			return resp.Content, nil
		}

		for _, call := range resp.ToolCalls {
			a.emit(Event{Step: step, Kind: EventToolCall, Tool: call.Name, Args: call.Arguments})
			callStart := time.Now()

			// Repeat guard: block identical calls so the model cannot spin.
			sig := call.Name + "\x00" + call.Arguments
			seen[sig]++
			if seen[sig] > repeatLimit {
				nudges++
				result := fmt.Sprintf(
					"blocked: you already called %s with identical arguments %d times. Change approach or give your final answer.",
					call.Name, seen[sig]-1)
				a.emit(Event{
					Step:       step,
					Kind:       EventToolResult,
					Tool:       call.Name,
					Result:     result,
					Err:        "repeated call blocked",
					DurationMS: time.Since(callStart).Milliseconds(),
				})
				msgs = a.appendMessage(msgs, llm.Message{Role: "tool", ToolCallID: call.ID, Content: result})
				if nudges >= maxRepeatNudges {
					msg := fmt.Sprintf("zcode: repeated tool calls (%s) without progress", call.Name)
					a.emit(Event{Step: step, Kind: EventLimit, Err: msg})
					return "", fmt.Errorf("%s", msg)
				}
				continue
			}

			decision, ok := a.checkPermission(call)
			if decision != "allow" {
				a.emit(Event{
					Step:       step,
					Kind:       EventPermission,
					Tool:       call.Name,
					Args:       call.Arguments,
					Result:     decision,
					Err:        "permission " + decision,
					DurationMS: time.Since(callStart).Milliseconds(),
				})
			}
			if !ok {
				result := fmt.Sprintf("blocked: permission denied for %s. Change approach or ask the user.", call.Name)
				msgs = a.appendMessage(msgs, llm.Message{Role: "tool", ToolCallID: call.ID, Content: result})
				continue
			}

			result, runErr := a.runTool(ctx, call)
			errStr := ""
			if runErr != nil {
				// Surface the failure to the model instead of aborting, so it
				// can correct course on the next step.
				result = "error: " + runErr.Error()
				errStr = runErr.Error()
			}
			a.emit(Event{
				Step:       step,
				Kind:       EventToolResult,
				Tool:       call.Name,
				Result:     result,
				Err:        errStr,
				DurationMS: time.Since(callStart).Milliseconds(),
			})
			msgs = a.appendMessage(msgs, llm.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    result,
			})
		}
	}

	msg := fmt.Sprintf("zcode: exceeded %d steps without a final answer", maxSteps)
	a.emit(Event{Step: maxSteps, Kind: EventLimit, Err: msg})
	return "", fmt.Errorf("%s", msg)
}

func (a *Agent) emit(e Event) {
	if a.OnEvent == nil {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if e.Mode == "" {
		e.Mode = a.Mode
	}
	a.OnEvent(e)
}

func (a *Agent) runTool(ctx context.Context, call llm.ToolCall) (string, error) {
	t, ok := a.allowed(call.Name)
	if !ok {
		return "", fmt.Errorf("unknown tool %q", call.Name)
	}
	return t.Run(ctx, call.Arguments)
}

func (a *Agent) toolSpecs() []llm.ToolSpec {
	if a.Tools == nil {
		return nil
	}
	all := a.Tools.All()
	specs := make([]llm.ToolSpec, 0, len(all))
	for _, t := range all {
		if a.Mode == ModePlan && !tools.IsPlanSafe(t) {
			continue
		}
		specs = append(specs, llm.ToolSpec{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Parameters(),
		})
	}
	return specs
}

// excerptRunes returns at most max runes of s, appending a marker when cut.
func excerptRunes(s string, max int) string {
	r := []rune(s)
	if max <= 0 || len(r) <= max {
		return s
	}
	return string(r[:max]) + "\n... [memory truncated] ..."
}
