package mcp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/zyong-zhou/zcode/internal/tools"
)

// startTimeout bounds the handshake and tool listing of a single server.
const startTimeout = 20 * time.Second

// clientFactory opens one server connection. It is a seam so tests can inject an
// in-memory client instead of spawning a process.
type clientFactory func(cfg ServerConfig, info ClientInfo) (Client, error)

// toolsChangedWatcher is implemented by clients that can report a server's
// tool-list changes.
type toolsChangedWatcher interface {
	OnToolsChanged(func())
}

// serverConn is one connected server and the tools it currently exposes.
type serverConn struct {
	cfg    ServerConfig
	client Client
	tools  []tools.Tool
}

// Manager owns a set of connected MCP servers and the tools they expose.
type Manager struct {
	mu       sync.Mutex
	servers  []*serverConn
	onChange func()
}

// Start connects to every server concurrently. A failing server is reported in
// errs but does not stop the others.
func Start(ctx context.Context, cfgs []ServerConfig, info ClientInfo) (*Manager, []error) {
	return startWith(ctx, cfgs, info, func(cfg ServerConfig, info ClientInfo) (Client, error) {
		return NewStdio(cfg, info)
	})
}

func startWith(ctx context.Context, cfgs []ServerConfig, info ClientInfo, newClient clientFactory) (*Manager, []error) {
	m := &Manager{}
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	for _, cfg := range cfgs {
		wg.Add(1)
		go func(cfg ServerConfig) {
			defer wg.Done()
			startCtx, cancel := context.WithTimeout(ctx, startTimeout)
			defer cancel()

			conn, err := startOne(startCtx, cfg, info, newClient)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("mcp %s: %w", cfg.Name, err))
				return
			}
			m.servers = append(m.servers, conn)
			m.watch(conn)
		}(cfg)
	}
	wg.Wait()
	return m, errs
}

func startOne(ctx context.Context, cfg ServerConfig, info ClientInfo, newClient clientFactory) (*serverConn, error) {
	c, err := newClient(cfg, info)
	if err != nil {
		return nil, err
	}
	if err := c.Initialize(ctx); err != nil {
		err = withStderr(c, err)
		_ = c.Close()
		return nil, err
	}
	ts, err := listTools(ctx, cfg, c)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	return &serverConn{cfg: cfg, client: c, tools: ts}, nil
}

// listTools lists a server's tools and wraps them as adapters.
func listTools(ctx context.Context, cfg ServerConfig, c Client) ([]tools.Tool, error) {
	infos, err := c.ListTools(ctx)
	if err != nil {
		return nil, withStderr(c, err)
	}
	out := make([]tools.Tool, 0, len(infos))
	for _, ti := range infos {
		out = append(out, NewTool(cfg.Name, c, ti, cfg.ReadOnly))
	}
	return out, nil
}

// watch asks a client to report tool-list changes. Refreshing runs off the
// read loop so it can issue its own request without deadlocking.
func (m *Manager) watch(conn *serverConn) {
	w, ok := conn.client.(toolsChangedWatcher)
	if !ok {
		return
	}
	w.OnToolsChanged(func() { go m.refresh(conn) })
}

// refresh re-lists one server's tools and calls the change hook.
func (m *Manager) refresh(conn *serverConn) {
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	ts, err := listTools(ctx, conn.cfg, conn.client)
	if err != nil {
		return
	}
	m.mu.Lock()
	conn.tools = ts
	onChange := m.onChange
	m.mu.Unlock()
	if onChange != nil {
		onChange()
	}
}

// SetOnChange registers a hook called after a server's tool list changes. It
// lets the caller re-register tools as servers come and go.
func (m *Manager) SetOnChange(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onChange = fn
}

// Tools returns adapters for every tool from every connected server.
func (m *Manager) Tools() []tools.Tool {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []tools.Tool
	for _, s := range m.servers {
		out = append(out, s.tools...)
	}
	return out
}

// ServerInfo summarises one connected server for status display.
type ServerInfo struct {
	Name     string
	Tools    int
	ReadOnly bool
}

// Servers returns a snapshot of the connected servers, in start order.
func (m *Manager) Servers() []ServerInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ServerInfo, 0, len(m.servers))
	for _, s := range m.servers {
		out = append(out, ServerInfo{Name: s.cfg.Name, Tools: len(s.tools), ReadOnly: s.cfg.ReadOnly})
	}
	return out
}

// Close shuts every server down and returns the first error.
func (m *Manager) Close() error {
	m.mu.Lock()
	servers := m.servers
	m.mu.Unlock()
	var first error
	for _, s := range servers {
		if err := s.client.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// stderrer is implemented by clients that capture the server's stderr.
type stderrer interface{ Stderr() string }

// withStderr appends a short, single-line view of the server's captured stderr
// to err so startup failures are diagnosable.
func withStderr(c Client, err error) error {
	s, ok := c.(stderrer)
	if !ok {
		return err
	}
	msg := strings.Join(strings.Fields(s.Stderr()), " ")
	if msg == "" {
		return err
	}
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:300]) + "..."
	}
	return fmt.Errorf("%w (stderr: %s)", err, msg)
}
