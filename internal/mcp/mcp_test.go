package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// memTransport is an in-memory io.ReadWriteCloser for driving a fake server.
type memTransport struct {
	cr *io.PipeReader
	cw *io.PipeWriter
}

func (t *memTransport) Read(p []byte) (int, error)  { return t.cr.Read(p) }
func (t *memTransport) Write(p []byte) (int, error) { return t.cw.Write(p) }
func (t *memTransport) Close() error {
	_ = t.cw.Close()
	return t.cr.Close()
}

// serveFakeOpts configures the in-memory fake server.
type serveFakeOpts struct {
	paginate       bool             // split tools/list across two pages
	root           string           // reported via roots/list
	serverRequests []map[string]any // requests the server sends right after initialize
	capture        chan rpcIncoming // replies to server-initiated requests
}

func newFakeClient(t *testing.T) *StdioClient {
	t.Helper()
	return newFakeClientWith(t, serveFakeOpts{})
}

func newFakeClientWith(t *testing.T, opts serveFakeOpts) *StdioClient {
	t.Helper()
	sr, cw := io.Pipe()
	cr, sw := io.Pipe()
	go serveFake(sr, sw, opts)
	c := newStdioClient("fake", ClientInfo{Name: "test", Version: "0", Root: opts.root}, &memTransport{cr: cr, cw: cw})
	go c.readLoop()
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func serveFake(sr *io.PipeReader, sw *io.PipeWriter, opts serveFakeOpts) {
	sc := bufio.NewScanner(sr)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var writeMu sync.Mutex
	write := func(v any) {
		writeMu.Lock()
		defer writeMu.Unlock()
		serverWrite(sw, v)
	}
	initialized := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg rpcIncoming
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg.Method == "" {
			// A response to a server-initiated request.
			if msg.ID != nil && opts.capture != nil {
				opts.capture <- msg
			}
			continue
		}
		if msg.ID == nil {
			continue // a notification
		}
		var req struct {
			Params struct {
				Cursor string `json:"cursor"`
				Name   string `json:"name"`
			} `json:"params"`
		}
		_ = json.Unmarshal([]byte(line), &req)

		var result any
		switch msg.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": protocolVersion,
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "fake"},
			}
		case "tools/list":
			result = toolsListResult(opts, req.Params.Cursor)
		case "tools/call":
			switch req.Params.Name {
			case "boom":
				result = map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": "bad"}}}
			case "empty":
				result = map[string]any{"content": []map[string]any{}}
			default:
				result = map[string]any{"content": []map[string]any{{"type": "text", "text": "called"}}}
			}
		default:
			write(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "error": map[string]any{"code": -32601, "message": "no"}})
			continue
		}
		write(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": result})
		if msg.Method == "initialize" && !initialized {
			initialized = true
			if len(opts.serverRequests) > 0 {
				// Send from a goroutine so this loop keeps reading the client's
				// replies; io.Pipe writes block until they are read.
				go func(reqs []map[string]any) {
					for _, r := range reqs {
						write(r)
					}
				}(opts.serverRequests)
			}
		}
	}
}

func toolsListResult(opts serveFakeOpts, cursor string) map[string]any {
	if opts.paginate {
		if cursor == "" {
			return map[string]any{
				"tools":      []map[string]any{{"name": "do.thing", "description": "does a thing", "inputSchema": map[string]any{"type": "object"}}},
				"nextCursor": "page2",
			}
		}
		return map[string]any{"tools": []map[string]any{{"name": "second_page"}}}
	}
	return map[string]any{"tools": []map[string]any{
		{"name": "do.thing", "description": "does a thing", "inputSchema": map[string]any{"type": "object"}},
		{"name": "read_only", "annotations": map[string]any{"readOnlyHint": true}},
	}}
}

func serverWrite(w io.Writer, v any) {
	data, _ := json.Marshal(v)
	_, _ = w.Write(append(data, '\n'))
}

func TestStdioHandshakeListAndCall(t *testing.T) {
	c := newFakeClient(t)
	ctx := context.Background()

	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	infos, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("tools = %d, want 2", len(infos))
	}
	if infos[0].Name != "do.thing" || infos[0].Description != "does a thing" {
		t.Errorf("tool 0 = %+v", infos[0])
	}
	if !infos[1].ReadOnly {
		t.Error("read_only tool should carry readOnlyHint")
	}

	res, err := c.CallTool(ctx, "do.thing", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "called" {
		t.Errorf("result = %+v", res)
	}
}

func TestListToolsPaginates(t *testing.T) {
	c := newFakeClientWith(t, serveFakeOpts{paginate: true})
	ctx := context.Background()
	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	infos, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(infos) != 2 || infos[0].Name != "do.thing" || infos[1].Name != "second_page" {
		t.Fatalf("infos = %+v", infos)
	}
}

func TestRequestError(t *testing.T) {
	c := newFakeClient(t)
	if _, err := c.request(context.Background(), "bogus/method", nil); err == nil || !strings.Contains(err.Error(), "bogus/method") {
		t.Errorf("err = %v", err)
	}
}

func TestServerInitiatedRequests(t *testing.T) {
	replies := make(chan rpcIncoming, 4)
	root := t.TempDir()
	c := newFakeClientWith(t, serveFakeOpts{
		root: root,
		serverRequests: []map[string]any{
			{"jsonrpc": "2.0", "id": "srv-ping", "method": "ping"},
			{"jsonrpc": "2.0", "id": "srv-roots", "method": "roots/list"},
			{"jsonrpc": "2.0", "id": "srv-unknown", "method": "sampling/createMessage"},
		},
		capture: replies,
	})
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	got := map[string]rpcIncoming{}
	for i := 0; i < 3; i++ {
		select {
		case m := <-replies:
			got[normalizeID(*m.ID)] = m
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for reply %d", i)
		}
	}

	if got["srv-ping"].Error != nil {
		t.Errorf("ping error = %v", got["srv-ping"].Error)
	}
	var roots struct {
		Roots []struct {
			URI string `json:"uri"`
		} `json:"roots"`
	}
	if err := json.Unmarshal(got["srv-roots"].Result, &roots); err != nil {
		t.Fatalf("decode roots: %v", err)
	}
	if len(roots.Roots) != 1 || !strings.HasPrefix(roots.Roots[0].URI, "file://") {
		t.Errorf("roots = %+v", roots)
	}
	if e := got["srv-unknown"].Error; e == nil || e.Code != -32601 {
		t.Errorf("unknown method error = %+v", e)
	}
}

func TestToolNameSanitizedAndBounded(t *testing.T) {
	if got, want := toolName("my server", "do.thing/v2"), "mcp__my_server__do_thing_v2"; got != want {
		t.Errorf("toolName = %q, want %q", got, want)
	}
	long := toolName("s", strings.Repeat("x", 200))
	if len(long) > maxToolNameLen {
		t.Errorf("len = %d, want <= %d", len(long), maxToolNameLen)
	}
	if !strings.HasPrefix(long, "mcp__s__") {
		t.Errorf("long name lost its namespace: %q", long)
	}
	if a, b := toolName("s", strings.Repeat("x", 200)), toolName("s", strings.Repeat("x", 201)); a == b {
		t.Error("distinct long names should not collide")
	}
}

func TestRenderContent(t *testing.T) {
	out := renderContent([]ContentBlock{
		{Type: "text", Text: "a"},
		{Type: "image", MimeType: "image/png"},
		{Type: "resource", URI: "file:///x"},
	})
	if out != "a\n[image image/png]\n[resource file:///x]" {
		t.Errorf("renderContent = %q", out)
	}
}

func TestToolPlanSafety(t *testing.T) {
	if !NewTool("s", nil, ToolInfo{Name: "x", ReadOnly: true}, false).PlanSafe() {
		t.Error("readOnlyHint should make a tool plan-safe")
	}
	if NewTool("s", nil, ToolInfo{Name: "y"}, false).PlanSafe() {
		t.Error("a plain tool must not be plan-safe by default")
	}
	if !NewTool("s", nil, ToolInfo{Name: "y"}, true).PlanSafe() {
		t.Error("a read_only server should make its tools plan-safe")
	}
}

func TestAdapterRun(t *testing.T) {
	c := newFakeClient(t)
	ctx := context.Background()
	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	ok := NewTool("fake", c, ToolInfo{Name: "do.thing"}, false)
	if out, err := ok.Run(ctx, `{"x":1}`); err != nil || out != "called" {
		t.Errorf("Run = %q, %v", out, err)
	}
	if out, err := ok.Run(ctx, ""); err != nil || out != "called" {
		t.Errorf("empty args Run = %q, %v", out, err)
	}

	boom := NewTool("fake", c, ToolInfo{Name: "boom"}, false)
	if out, err := boom.Run(ctx, "{}"); err != nil || out != "error: bad" {
		t.Errorf("isError Run = %q, %v", out, err)
	}

	empty := NewTool("fake", c, ToolInfo{Name: "empty"}, false)
	if out, err := empty.Run(ctx, "{}"); err != nil || out != "(no content)" {
		t.Errorf("empty content Run = %q, %v", out, err)
	}

	off := NewTool("fake", nil, ToolInfo{Name: "x"}, false)
	if _, err := off.Run(ctx, "{}"); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Errorf("nil client err = %v", err)
	}
}

// fakeMCPClient is a Client with no process behind it, for manager tests.
type fakeMCPClient struct {
	infos    []ToolInfo
	onChange func()
	closed   bool
}

func (f *fakeMCPClient) Initialize(context.Context) error              { return nil }
func (f *fakeMCPClient) ListTools(context.Context) ([]ToolInfo, error) { return f.infos, nil }
func (f *fakeMCPClient) CallTool(context.Context, string, json.RawMessage) (CallResult, error) {
	return CallResult{}, nil
}
func (f *fakeMCPClient) Close() error             { f.closed = true; return nil }
func (f *fakeMCPClient) OnToolsChanged(fn func()) { f.onChange = fn }

func TestManagerStartReportsBadServer(t *testing.T) {
	m, errs := Start(context.Background(), []ServerConfig{{Name: "empty"}}, ClientInfo{})
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want 1", errs)
	}
	if len(m.Tools()) != 0 {
		t.Errorf("tools = %d, want 0", len(m.Tools()))
	}
	if err := m.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestManagerWithFactory(t *testing.T) {
	good := &fakeMCPClient{infos: []ToolInfo{{Name: "a"}, {Name: "b"}}}
	factory := func(cfg ServerConfig, _ ClientInfo) (Client, error) {
		if cfg.Name == "bad" {
			return nil, errors.New("boom")
		}
		return good, nil
	}
	m, errs := startWith(context.Background(), []ServerConfig{{Name: "ok"}, {Name: "bad"}}, ClientInfo{}, factory)
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want 1", errs)
	}
	if got := len(m.Tools()); got != 2 {
		t.Errorf("tools = %d, want 2", got)
	}
	if err := m.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if !good.closed {
		t.Error("client was not closed")
	}
}

func TestManagerRefreshOnToolsChanged(t *testing.T) {
	cl := &fakeMCPClient{infos: []ToolInfo{{Name: "a"}}}
	factory := func(ServerConfig, ClientInfo) (Client, error) { return cl, nil }
	m, errs := startWith(context.Background(), []ServerConfig{{Name: "s"}}, ClientInfo{}, factory)
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if cl.onChange == nil {
		t.Fatal("tools-changed hook was not wired")
	}

	cl.infos = []ToolInfo{{Name: "a"}, {Name: "b"}}
	done := make(chan struct{}, 1)
	m.SetOnChange(func() { done <- struct{}{} })
	cl.onChange()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("refresh did not notify")
	}
	if got := len(m.Tools()); got != 2 {
		t.Errorf("tools after refresh = %d, want 2", got)
	}
}

type stderrClient struct{ msg string }

func (s *stderrClient) Initialize(context.Context) error              { return nil }
func (s *stderrClient) ListTools(context.Context) ([]ToolInfo, error) { return nil, nil }
func (s *stderrClient) CallTool(context.Context, string, json.RawMessage) (CallResult, error) {
	return CallResult{}, nil
}
func (s *stderrClient) Close() error   { return nil }
func (s *stderrClient) Stderr() string { return s.msg }

func TestWithStderr(t *testing.T) {
	base := errors.New("handshake failed")
	if got := withStderr(&fakeMCPClient{}, base); got != base {
		t.Errorf("non-stderrer should pass through: %v", got)
	}
	got := withStderr(&stderrClient{msg: "boom\n  detail  "}, base)
	if !strings.Contains(got.Error(), "boom detail") || !errors.Is(got, base) {
		t.Errorf("withStderr = %v", got)
	}
}
