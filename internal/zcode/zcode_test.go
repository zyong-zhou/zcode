package zcode_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zyong-zhou/zcode/internal/llm"
	"github.com/zyong-zhou/zcode/internal/tools"
	"github.com/zyong-zhou/zcode/internal/zcode"
)

// fakeClient replays canned responses and counts calls. It must never perform
// real network I/O.
type fakeClient struct {
	responses []llm.Response
	calls     int
}

func (f *fakeClient) Chat(_ context.Context, _ llm.Request) (llm.Response, error) {
	r := f.responses[f.calls]
	f.calls++
	return r, nil
}

type echoTool struct{}

func (echoTool) Name() string                                    { return "echo" }
func (echoTool) Description() string                             { return "echo back" }
func (echoTool) Parameters() map[string]any                      { return map[string]any{"type": "object"} }
func (echoTool) Run(_ context.Context, a string) (string, error) { return "echo:" + a, nil }

func TestAgentRunsToolThenAnswers(t *testing.T) {
	fc := &fakeClient{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "1", Name: "echo", Arguments: `{"x":1}`}}},
		{Content: "final design"},
	}}
	a := zcode.New(fc, tools.NewRegistry(echoTool{}), zcode.Options{System: "system"})

	got, err := a.Design(context.Background(), "design a thing")
	if err != nil {
		t.Fatalf("Design: %v", err)
	}
	if got != "final design" {
		t.Errorf("got %q, want final design", got)
	}
	if fc.calls != 2 {
		t.Errorf("calls = %d, want 2", fc.calls)
	}
}

func TestAgentStopsAtMaxSteps(t *testing.T) {
	fc := &fakeClient{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "1", Name: "echo", Arguments: "{}"}}},
	}}
	a := zcode.New(fc, tools.NewRegistry(echoTool{}), zcode.Options{System: "system"})
	a.MaxSteps = 1

	if _, err := a.Design(context.Background(), "x"); err == nil {
		t.Fatal("expected error when MaxSteps is exhausted")
	}
}

func TestAgentUnknownToolIsReported(t *testing.T) {
	fc := &fakeClient{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "1", Name: "nope", Arguments: "{}"}}},
		{Content: "recovered"},
	}}
	a := zcode.New(fc, tools.NewRegistry(echoTool{}), zcode.Options{System: "system"})

	got, err := a.Design(context.Background(), "x")
	if err != nil {
		t.Fatalf("Design: %v", err)
	}
	if got != "recovered" {
		t.Errorf("got %q, want recovered", got)
	}
}

func TestAgentEmitsEventsAndUsage(t *testing.T) {
	fc := &fakeClient{responses: []llm.Response{
		{
			Usage:     llm.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
			ToolCalls: []llm.ToolCall{{ID: "1", Name: "echo", Arguments: `{"x":1}`}},
		},
		{
			Usage:   llm.Usage{PromptTokens: 20, CompletionTokens: 3, TotalTokens: 23},
			Content: "final design",
		},
	}}

	var kinds []zcode.EventKind
	var calledTools []string
	a := zcode.New(fc, tools.NewRegistry(echoTool{}), zcode.Options{System: "system"})
	a.OnEvent = func(e zcode.Event) {
		kinds = append(kinds, e.Kind)
		if e.Kind == zcode.EventToolCall {
			calledTools = append(calledTools, e.Tool)
		}
	}

	if _, err := a.Design(context.Background(), "x"); err != nil {
		t.Fatalf("Design: %v", err)
	}

	want := []zcode.EventKind{
		zcode.EventThought, zcode.EventToolCall, zcode.EventToolResult,
		zcode.EventAssistant, zcode.EventFinal,
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Errorf("event kinds = %v, want %v", kinds, want)
	}
	if len(calledTools) != 1 || calledTools[0] != "echo" {
		t.Errorf("tool call events = %v", calledTools)
	}
	if a.Usage != (llm.Usage{PromptTokens: 30, CompletionTokens: 5, TotalTokens: 35}) {
		t.Errorf("usage = %+v", a.Usage)
	}
}

func TestAgentEmitsToolError(t *testing.T) {
	fc := &fakeClient{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "1", Name: "nope", Arguments: "{}"}}},
		{Content: "recovered"},
	}}

	var toolErr string
	a := zcode.New(fc, tools.NewRegistry(echoTool{}), zcode.Options{System: "system"})
	a.OnEvent = func(e zcode.Event) {
		if e.Kind == zcode.EventToolResult {
			toolErr = e.Err
		}
	}

	if _, err := a.Design(context.Background(), "x"); err != nil {
		t.Fatalf("Design: %v", err)
	}
	if !strings.Contains(toolErr, "unknown tool") {
		t.Errorf("tool error event = %q, want it to mention unknown tool", toolErr)
	}
}

// recordingClient captures each request so tests can assert conversation state.
type recordingClient struct {
	responses []llm.Response
	requests  []llm.Request
}

func (c *recordingClient) Chat(_ context.Context, req llm.Request) (llm.Response, error) {
	c.requests = append(c.requests, req)
	return c.responses[len(c.requests)-1], nil
}

func rolesOf(msgs []llm.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Role
	}
	return out
}

func TestAgentAskKeepsHistory(t *testing.T) {
	rc := &recordingClient{responses: []llm.Response{{Content: "first"}, {Content: "second"}}}
	a := zcode.New(rc, tools.NewRegistry(echoTool{}), zcode.Options{System: "system"})

	if _, err := a.Ask(context.Background(), "q1"); err != nil {
		t.Fatalf("Ask 1: %v", err)
	}
	if _, err := a.Ask(context.Background(), "q2"); err != nil {
		t.Fatalf("Ask 2: %v", err)
	}

	msgs := rc.requests[1].Messages
	want := []string{"system", "user", "assistant", "user"}
	if got := rolesOf(msgs); !reflect.DeepEqual(got, want) {
		t.Fatalf("roles = %v, want %v", got, want)
	}
	if msgs[1].Content != "q1" || msgs[3].Content != "q2" {
		t.Errorf("contents = %q, %q; want q1, q2", msgs[1].Content, msgs[3].Content)
	}
}

func TestAgentResetClearsHistory(t *testing.T) {
	rc := &recordingClient{responses: []llm.Response{{Content: "a"}, {Content: "b"}}}
	a := zcode.New(rc, tools.NewRegistry(echoTool{}), zcode.Options{System: "system"})

	_, _ = a.Ask(context.Background(), "q1")
	a.Reset()
	_, _ = a.Ask(context.Background(), "q2")

	if got := len(rc.requests[1].Messages); got != 2 {
		t.Errorf("messages after reset = %d, want 2 (system+user)", got)
	}
}

func TestAgentDesignResetsHistory(t *testing.T) {
	rc := &recordingClient{responses: []llm.Response{{Content: "a"}, {Content: "b"}}}
	a := zcode.New(rc, tools.NewRegistry(echoTool{}), zcode.Options{System: "system"})

	_, _ = a.Ask(context.Background(), "q1")
	_, _ = a.Design(context.Background(), "q2")

	if got := len(rc.requests[1].Messages); got != 2 {
		t.Errorf("messages after Design = %d, want 2 (system+user)", got)
	}
}

// streamClient implements both llm.Client and llm.StreamClient so tests can
// verify that deltas are forwarded and the assembled answer is used.
type streamClient struct {
	responses []llm.Response
	deltas    [][]string
	i         int
}

func (c *streamClient) Chat(_ context.Context, _ llm.Request) (llm.Response, error) {
	return c.responses[c.i], nil
}

func (c *streamClient) ChatStream(_ context.Context, _ llm.Request, onDelta func(string)) (llm.Response, error) {
	resp := c.responses[c.i]
	for _, d := range c.deltas[c.i] {
		onDelta(d)
	}
	c.i++
	return resp, nil
}

func TestAgentStreamsDeltas(t *testing.T) {
	sc := &streamClient{
		responses: []llm.Response{{Content: "Hello"}},
		deltas:    [][]string{{"Hel", "lo"}},
	}
	a := zcode.New(sc, tools.NewRegistry(echoTool{}), zcode.Options{System: "system"})

	var got []string
	a.OnDelta = func(s string) { got = append(got, s) }

	out, err := a.Design(context.Background(), "x")
	if err != nil {
		t.Fatalf("Design: %v", err)
	}
	if out != "Hello" {
		t.Errorf("answer = %q, want Hello", out)
	}
	if joined := strings.Join(got, ""); joined != "Hello" {
		t.Errorf("deltas = %v (joined %q)", got, joined)
	}
}

// memStub is a MemoryProvider for tests.
type memStub struct{ text string }

func (m memStub) Render() string { return m.text }

func TestAgentInjectsMemoryIntoSystem(t *testing.T) {
	rc := &recordingClient{responses: []llm.Response{{Content: "ok"}}}
	a := zcode.New(rc, tools.NewRegistry(echoTool{}), zcode.Options{
		System: "base prompt",
		Memory: memStub{text: "- 使用 Go 1.26"},
	})

	if _, err := a.Design(context.Background(), "x"); err != nil {
		t.Fatalf("Design: %v", err)
	}
	sys := rc.requests[0].Messages[0]
	if sys.Role != "system" {
		t.Fatalf("first message role = %q, want system", sys.Role)
	}
	if !strings.Contains(sys.Content, "base prompt") || !strings.Contains(sys.Content, "使用 Go 1.26") {
		t.Errorf("system message = %q", sys.Content)
	}
}

func TestAgentInjectsRulesBeforeMemory(t *testing.T) {
	rc := &recordingClient{responses: []llm.Response{{Content: "ok"}}}
	a := zcode.New(rc, tools.NewRegistry(echoTool{}), zcode.Options{
		System: "base prompt",
		Rules:  memStub{text: "规则：用 gofmt"},
		Memory: memStub{text: "记忆：用 Go 1.26"},
	})

	if _, err := a.Design(context.Background(), "x"); err != nil {
		t.Fatalf("Design: %v", err)
	}
	sys := rc.requests[0].Messages[0].Content
	ri := strings.Index(sys, "规则：用 gofmt")
	mi := strings.Index(sys, "记忆：用 Go 1.26")
	if ri < 0 || mi < 0 {
		t.Fatalf("system message missing injected text: %q", sys)
	}
	if ri > mi {
		t.Errorf("rules should be injected before memory:\n%s", sys)
	}
}

// countTool records how many times it ran, to prove whether a call was gated.
type countTool struct{ n *int }

func (countTool) Name() string                                      { return "echo" }
func (countTool) Description() string                               { return "counts" }
func (countTool) Parameters() map[string]any                        { return map[string]any{"type": "object"} }
func (c countTool) Run(_ context.Context, _ string) (string, error) { *c.n++; return "ok", nil }

func TestAgentPermissionGate(t *testing.T) {
	ctx := context.Background()
	responses := func() []llm.Response {
		return []llm.Response{
			{ToolCalls: []llm.ToolCall{{ID: "1", Name: "echo", Arguments: "{}"}}},
			{Content: "done"},
		}
	}

	// deny blocks the call.
	var n int
	var permResult string
	a := zcode.New(&fakeClient{responses: responses()}, tools.NewRegistry(countTool{n: &n}), zcode.Options{
		System:      "s",
		Permissions: func(string, string) string { return "deny" },
	})
	a.OnEvent = func(e zcode.Event) {
		if e.Kind == zcode.EventPermission {
			permResult = e.Result
		}
	}
	if _, err := a.Design(ctx, "x"); err != nil {
		t.Fatalf("Design: %v", err)
	}
	if n != 0 {
		t.Errorf("denied tool ran %d times, want 0", n)
	}
	if permResult != "deny" {
		t.Errorf("permission event result = %q, want deny", permResult)
	}

	// ask with an approving handler runs the call.
	n = 0
	asked := false
	a2 := zcode.New(&fakeClient{responses: responses()}, tools.NewRegistry(countTool{n: &n}), zcode.Options{
		System:       "s",
		Permissions:  func(string, string) string { return "ask" },
		OnPermission: func(zcode.PermissionRequest) bool { asked = true; return true },
	})
	if _, err := a2.Design(ctx, "x"); err != nil {
		t.Fatalf("Design: %v", err)
	}
	if !asked || n != 1 {
		t.Errorf("ask+approve: asked=%v ran=%d, want true/1", asked, n)
	}

	// ask with no handler is denied (non-interactive safety).
	n = 0
	a3 := zcode.New(&fakeClient{responses: responses()}, tools.NewRegistry(countTool{n: &n}), zcode.Options{
		System:      "s",
		Permissions: func(string, string) string { return "ask" },
	})
	if _, err := a3.Design(ctx, "x"); err != nil {
		t.Fatalf("Design: %v", err)
	}
	if n != 0 {
		t.Errorf("ask with no handler ran %d times, want 0", n)
	}
}

// previewTool implements the optional tools.Previewer interface.
type previewTool struct{ ran *int }

func (previewTool) Name() string               { return "preview_tool" }
func (previewTool) Description() string        { return "preview" }
func (previewTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (p previewTool) Run(context.Context, string) (string, error) {
	*p.ran++
	return "ok", nil
}
func (previewTool) Preview(args string) (string, bool) { return "PREVIEW:" + args, true }

func TestAgentPermissionRequestCarriesPreview(t *testing.T) {
	var got zcode.PermissionRequest
	a := zcode.New(&fakeClient{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "1", Name: "preview_tool", Arguments: `{"x":1}`}}},
		{Content: "done"},
	}}, tools.NewRegistry(previewTool{ran: new(int)}), zcode.Options{
		System:       "s",
		Permissions:  func(string, string) string { return "ask" },
		OnPermission: func(req zcode.PermissionRequest) bool { got = req; return true },
	})
	if _, err := a.Design(context.Background(), "x"); err != nil {
		t.Fatalf("Design: %v", err)
	}
	if got.Preview != `PREVIEW:{"x":1}` {
		t.Errorf("preview = %q, want the tool preview", got.Preview)
	}
}

// fakeCompactor replaces the history with its first message when changed.
type fakeCompactor struct{ changed bool }

func (f fakeCompactor) Compact(_ context.Context, msgs []llm.Message) ([]llm.Message, bool, error) {
	if !f.changed {
		return msgs, false, nil
	}
	if len(msgs) == 0 {
		return msgs, false, nil
	}
	return msgs[:1], true, nil
}

func TestAgentHistoryIsCopyAndCompact(t *testing.T) {
	ctx := context.Background()
	a := zcode.New(&fakeClient{responses: []llm.Response{{Content: "hi"}}}, tools.NewRegistry(), zcode.Options{System: "s"})
	if _, err := a.Ask(ctx, "q"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	h := a.History()
	if len(h) == 0 {
		t.Fatal("history is empty after Ask")
	}
	h[0] = llm.Message{Role: "user", Content: "mutated"}
	if a.History()[0].Content == "mutated" {
		t.Error("History returned a view, not a copy")
	}

	a.Compactor = fakeCompactor{changed: false}
	if changed, err := a.Compact(ctx); err != nil || changed {
		t.Errorf("Compact(no change) = %v, %v; want false, nil", changed, err)
	}
	before := len(a.History())
	a.Compactor = fakeCompactor{changed: true}
	changed, err := a.Compact(ctx)
	if err != nil || !changed {
		t.Fatalf("Compact = %v, %v; want true, nil", changed, err)
	}
	if after := len(a.History()); after >= before || after != 1 {
		t.Errorf("history after compact = %d messages, want 1 (< %d)", after, before)
	}
}

// memTranscript is an in-memory Transcript for tests.
type memTranscript struct{ msgs []llm.Message }

func (m *memTranscript) Load() ([]llm.Message, error) {
	return append([]llm.Message(nil), m.msgs...), nil
}
func (m *memTranscript) Append(msg llm.Message) error { m.msgs = append(m.msgs, msg); return nil }
func (m *memTranscript) Reset() error                 { m.msgs = nil; return nil }

func TestAgentRecordsTranscript(t *testing.T) {
	tr := &memTranscript{}
	fc := &fakeClient{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "1", Name: "echo", Arguments: "{}"}}},
		{Content: "final"},
	}}
	a := zcode.New(fc, tools.NewRegistry(echoTool{}), zcode.Options{System: "s", Transcript: tr})

	if _, err := a.Design(context.Background(), "hi"); err != nil {
		t.Fatalf("Design: %v", err)
	}
	var roles []string
	for _, m := range tr.msgs {
		roles = append(roles, m.Role)
	}
	want := []string{"user", "assistant", "tool", "assistant"}
	if !reflect.DeepEqual(roles, want) {
		t.Errorf("transcript roles = %v, want %v", roles, want)
	}
}

func TestAgentRestoreAndReset(t *testing.T) {
	tr := &memTranscript{msgs: []llm.Message{
		{Role: "user", Content: "早"},
		{Role: "assistant", Content: "好"},
	}}
	rc := &recordingClient{responses: []llm.Response{{Content: "继续"}}}
	a := zcode.New(rc, tools.NewRegistry(echoTool{}), zcode.Options{System: "s", Transcript: tr})

	if err := a.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, err := a.Ask(context.Background(), "在吗"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	msgs := rc.requests[0].Messages
	if len(msgs) != 4 || msgs[0].Role != "system" || msgs[1].Content != "早" || msgs[3].Content != "在吗" {
		t.Fatalf("restored request = %+v", msgs)
	}

	a.Reset()
	if len(tr.msgs) != 0 {
		t.Errorf("Reset should clear the transcript, got %d messages", len(tr.msgs))
	}
}

// shrinkCompactor collapses everything after the system message into a summary.
type shrinkCompactor struct{ called int }

func (s *shrinkCompactor) Compact(_ context.Context, msgs []llm.Message) ([]llm.Message, bool, error) {
	s.called++
	if len(msgs) == 0 {
		return msgs, false, nil
	}
	return []llm.Message{msgs[0], {Role: "user", Content: "summary"}}, true, nil
}

func TestAgentCompactsHistory(t *testing.T) {
	rc := &recordingClient{responses: []llm.Response{{Content: "a"}, {Content: "b"}}}
	c := &shrinkCompactor{}
	a := zcode.New(rc, tools.NewRegistry(echoTool{}), zcode.Options{System: "s", Compactor: c})
	var compacted bool
	a.OnEvent = func(e zcode.Event) {
		if e.Kind == zcode.EventCompact {
			compacted = true
		}
	}

	if _, err := a.Ask(context.Background(), "first"); err != nil {
		t.Fatalf("first Ask: %v", err)
	}
	if _, err := a.Ask(context.Background(), "second"); err != nil {
		t.Fatalf("second Ask: %v", err)
	}
	if c.called == 0 || !compacted {
		t.Errorf("compactor called=%d compacted event=%v", c.called, compacted)
	}
	last := rc.requests[len(rc.requests)-1].Messages
	found := false
	for _, m := range last {
		if m.Content == "summary" {
			found = true
		}
	}
	if !found {
		t.Errorf("compacted summary missing from request: %+v", last)
	}
}

// readLike is plan-safe; writeLike is not (it does not implement PlanSafe).
type readLike struct{}

func (readLike) Name() string                                    { return "read_like" }
func (readLike) Description() string                             { return "reads" }
func (readLike) Parameters() map[string]any                      { return map[string]any{"type": "object"} }
func (readLike) PlanSafe() bool                                  { return true }
func (readLike) Run(_ context.Context, _ string) (string, error) { return "read", nil }

type writeLike struct{}

func (writeLike) Name() string                                    { return "write_like" }
func (writeLike) Description() string                             { return "writes" }
func (writeLike) Parameters() map[string]any                      { return map[string]any{"type": "object"} }
func (writeLike) Run(_ context.Context, _ string) (string, error) { return "wrote", nil }

func TestAgentPlanModeFiltersTools(t *testing.T) {
	rc := &recordingClient{responses: []llm.Response{{Content: "ok"}}}
	a := zcode.New(rc, tools.NewRegistry(readLike{}, writeLike{}), zcode.Options{
		System: "s",
		Mode:   zcode.ModePlan,
	})

	if _, err := a.Design(context.Background(), "x"); err != nil {
		t.Fatalf("Design: %v", err)
	}
	names := map[string]bool{}
	for _, spec := range rc.requests[0].Tools {
		names[spec.Name] = true
	}
	if !names["read_like"] {
		t.Errorf("plan mode should expose read_like: %v", names)
	}
	if names["write_like"] {
		t.Errorf("plan mode should hide write_like: %v", names)
	}
}

func TestAgentPlanModeBlocksMutatingCall(t *testing.T) {
	fc := &fakeClient{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "1", Name: "write_like", Arguments: "{}"}}},
		{Content: "done"},
	}}
	a := zcode.New(fc, tools.NewRegistry(writeLike{}), zcode.Options{System: "s", Mode: zcode.ModePlan})

	var toolErr string
	a.OnEvent = func(e zcode.Event) {
		if e.Kind == zcode.EventToolResult {
			toolErr = e.Err
		}
	}
	if _, err := a.Design(context.Background(), "x"); err != nil {
		t.Fatalf("Design: %v", err)
	}
	if !strings.Contains(toolErr, "unknown tool") {
		t.Errorf("tool error = %q, want it to mention unknown tool", toolErr)
	}
}

// repeatingClient always asks for the same tool call.
type repeatingClient struct{}

func (repeatingClient) Chat(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{ToolCalls: []llm.ToolCall{{ID: "1", Name: "echo", Arguments: "{}"}}}, nil
}

func TestAgentRepeatGuardStopsRun(t *testing.T) {
	a := zcode.New(repeatingClient{}, tools.NewRegistry(echoTool{}), zcode.Options{
		System:      "s",
		MaxSteps:    20,
		RepeatLimit: 2,
	})
	if _, err := a.Design(context.Background(), "x"); err == nil {
		t.Fatal("expected the repeat guard to stop the run")
	}
}

// slowClient never answers, so a timeout is the only way out.
type slowClient struct{}

func (slowClient) Chat(ctx context.Context, _ llm.Request) (llm.Response, error) {
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

func TestAgentTimeoutStopsRun(t *testing.T) {
	a := zcode.New(slowClient{}, tools.NewRegistry(echoTool{}), zcode.Options{
		System:  "s",
		Timeout: 20 * time.Millisecond,
	})
	_, err := a.Design(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
}
