package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// protocolVersion is the MCP revision this client speaks.
	protocolVersion = "2025-06-18"
	// scanMaxBytes bounds a single JSON-RPC message.
	scanMaxBytes = 10 << 20
	// closeGrace bounds how long Close waits for the child to exit.
	closeGrace = 2 * time.Second
)

// StdioClient speaks newline-delimited JSON-RPC to an MCP server over a
// transport. The transport is an interface so tests can inject a fake server.
type StdioClient struct {
	name      string
	info      ClientInfo
	transport io.ReadWriteCloser
	cmd       *exec.Cmd

	writeMu sync.Mutex
	mu      sync.Mutex
	nextID  int64
	pending map[string]chan rpcIncoming

	done      chan struct{}
	closeOnce sync.Once
	readErr   error
	stderr    strings.Builder

	// onToolsChanged is invoked when the server announces a tool-list change.
	onToolsChanged func()
}

// NewStdio starts cfg.Command and returns a client ready for Initialize.
func NewStdio(cfg ServerConfig, info ClientInfo) (*StdioClient, error) {
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, fmt.Errorf("mcp %s: command is required", cfg.Name)
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = os.Environ()
	for k, v := range cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp %s: stdin: %w", cfg.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp %s: stdout: %w", cfg.Name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp %s: stderr: %w", cfg.Name, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp %s: start: %w", cfg.Name, err)
	}

	c := newStdioClient(cfg.Name, info, &stdioTransport{r: stdout, w: stdin})
	c.cmd = cmd
	go c.drainStderr(stderr)
	go c.readLoop()
	return c, nil
}

func newStdioClient(name string, info ClientInfo, tr io.ReadWriteCloser) *StdioClient {
	return &StdioClient{
		name:      name,
		info:      info,
		transport: tr,
		pending:   map[string]chan rpcIncoming{},
		done:      make(chan struct{}),
	}
}

// stdioTransport joins a child's stdout/stderr reads with its stdin writes.
type stdioTransport struct {
	r io.ReadCloser
	w io.WriteCloser
}

func (t *stdioTransport) Read(p []byte) (int, error)  { return t.r.Read(p) }
func (t *stdioTransport) Write(p []byte) (int, error) { return t.w.Write(p) }
func (t *stdioTransport) Close() error {
	err := t.w.Close()
	if rErr := t.r.Close(); err == nil {
		err = rErr
	}
	return err
}

// Initialize implements Client.
func (c *StdioClient) Initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": c.info.Name, "version": c.info.Version},
	}
	if _, err := c.request(ctx, "initialize", params); err != nil {
		return err
	}
	return c.notify("notifications/initialized", map[string]any{})
}

// ListTools implements Client, following pagination cursors.
func (c *StdioClient) ListTools(ctx context.Context) ([]ToolInfo, error) {
	var out []ToolInfo
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.request(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var res struct {
			Tools []struct {
				Name        string         `json:"name"`
				Description string         `json:"description"`
				InputSchema map[string]any `json:"inputSchema"`
				Annotations struct {
					ReadOnlyHint bool `json:"readOnlyHint"`
				} `json:"annotations"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, fmt.Errorf("mcp %s: tools/list: %w", c.name, err)
		}
		for _, t := range res.Tools {
			out = append(out, ToolInfo{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: t.InputSchema,
				ReadOnly:    t.Annotations.ReadOnlyHint,
			})
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return out, nil
}

// CallTool implements Client.
func (c *StdioClient) CallTool(ctx context.Context, name string, args json.RawMessage) (CallResult, error) {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	params := map[string]any{"name": name, "arguments": args}
	raw, err := c.request(ctx, "tools/call", params)
	if err != nil {
		return CallResult{}, err
	}
	var res struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
			URI      string `json:"uri"`
			Resource *struct {
				URI      string `json:"uri"`
				MimeType string `json:"mimeType"`
			} `json:"resource"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return CallResult{}, fmt.Errorf("mcp %s: tools/call: %w", c.name, err)
	}
	out := CallResult{IsError: res.IsError}
	for _, b := range res.Content {
		cb := ContentBlock{Type: b.Type, Text: b.Text, Data: b.Data, MimeType: b.MimeType, URI: b.URI}
		if b.Resource != nil {
			cb.URI = b.Resource.URI
			cb.MimeType = b.Resource.MimeType
		}
		out.Content = append(out.Content, cb)
	}
	return out, nil
}

// Close terminates the transport and the child process, if any.
func (c *StdioClient) Close() error {
	c.shutdown()
	_ = c.transport.Close()
	if c.cmd != nil && c.cmd.Process != nil {
		waited := make(chan struct{})
		go func() { _ = c.cmd.Wait(); close(waited) }()
		select {
		case <-waited:
		case <-time.After(closeGrace):
			_ = c.cmd.Process.Kill()
			<-waited
		}
	}
	return nil
}

// Stderr returns captured server stderr, useful for diagnostics.
func (c *StdioClient) Stderr() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stderr.String()
}

// request sends a JSON-RPC request and waits for its response.
func (c *StdioClient) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id, ch := c.register()
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	if err := c.writeLine(req); err != nil {
		c.unregister(id)
		return nil, err
	}

	select {
	case <-ctx.Done():
		c.unregister(id)
		return nil, ctx.Err()
	case <-c.done:
		return nil, fmt.Errorf("mcp %s: connection closed: %v", c.name, c.err())
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("mcp %s: %s: %s", c.name, method, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (c *StdioClient) notify(method string, params any) error {
	return c.writeLine(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *StdioClient) register() (int64, chan rpcIncoming) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := c.nextID
	ch := make(chan rpcIncoming, 1)
	c.pending[strconv.FormatInt(id, 10)] = ch
	return id, ch
}

func (c *StdioClient) unregister(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.pending, strconv.FormatInt(id, 10))
}

func (c *StdioClient) writeLine(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.transport.Write(append(data, '\n'))
	return err
}

func (c *StdioClient) readLoop() {
	sc := bufio.NewScanner(c.transport)
	sc.Buffer(make([]byte, 0, 64<<10), scanMaxBytes)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg rpcIncoming
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue // ignore malformed lines rather than dropping the server
		}
		c.dispatch(msg)
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	c.mu.Lock()
	c.readErr = err
	c.mu.Unlock()
	c.shutdown()
}

// OnToolsChanged registers a callback invoked when the server announces a tool
// list change (notifications/tools/list_changed).
func (c *StdioClient) OnToolsChanged(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onToolsChanged = fn
}

func (c *StdioClient) dispatch(msg rpcIncoming) {
	if msg.Method != "" {
		// A request or notification initiated by the server.
		if msg.ID != nil {
			c.handleServerRequest(msg)
		} else {
			c.handleNotification(msg.Method)
		}
		return
	}
	if msg.ID == nil {
		return
	}
	key := normalizeID(*msg.ID)
	c.mu.Lock()
	ch := c.pending[key]
	delete(c.pending, key)
	c.mu.Unlock()
	if ch != nil {
		ch <- msg
	}
}

func (c *StdioClient) handleServerRequest(msg rpcIncoming) {
	switch msg.Method {
	case "ping":
		c.respond(*msg.ID, map[string]any{}, nil)
	case "roots/list":
		var roots []map[string]any
		if c.info.Root != "" {
			roots = append(roots, map[string]any{"uri": fileURI(c.info.Root), "name": "project"})
		}
		c.respond(*msg.ID, map[string]any{"roots": roots}, nil)
	default:
		c.respond(*msg.ID, nil, &rpcError{Code: -32601, Message: "method not found: " + msg.Method})
	}
}

// handleNotification reacts to server notifications we care about. Unknown
// notifications are ignored.
func (c *StdioClient) handleNotification(method string) {
	if method != "notifications/tools/list_changed" {
		return
	}
	c.mu.Lock()
	fn := c.onToolsChanged
	c.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (c *StdioClient) respond(id json.RawMessage, result any, rerr *rpcError) {
	out := map[string]any{"jsonrpc": "2.0", "id": id}
	if rerr != nil {
		out["error"] = rerr
	} else {
		out["result"] = result
	}
	_ = c.writeLine(out)
}

func (c *StdioClient) drainStderr(r io.Reader) {
	b, _ := io.ReadAll(io.LimitReader(r, 64<<10))
	if len(b) == 0 {
		return
	}
	c.mu.Lock()
	c.stderr.Write(b)
	c.mu.Unlock()
}

func (c *StdioClient) shutdown() {
	c.closeOnce.Do(func() { close(c.done) })
}

func (c *StdioClient) err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readErr
}

func normalizeID(raw json.RawMessage) string {
	return strings.Trim(strings.TrimSpace(string(raw)), `"`)
}

func fileURI(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	abs = filepath.ToSlash(abs)
	if !strings.HasPrefix(abs, "/") {
		abs = "/" + abs
	}
	return "file://" + abs
}

type rpcIncoming struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return e.Message }
