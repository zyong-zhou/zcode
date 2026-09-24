package mcp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestStdioOnToolsChanged(t *testing.T) {
	c := newFakeClient(t)
	called := make(chan struct{}, 1)
	c.OnToolsChanged(func() { called <- struct{}{} })

	c.dispatch(rpcIncoming{JSONRPC: "2.0", Method: "notifications/tools/list_changed"})
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("OnToolsChanged hook was not invoked")
	}

	// Unrelated notifications are ignored and must not panic.
	c.dispatch(rpcIncoming{Method: "notifications/other"})
}

func TestStdioStderrCapture(t *testing.T) {
	c := newFakeClient(t)
	c.drainStderr(strings.NewReader("boom detail"))
	if got := c.Stderr(); !strings.Contains(got, "boom detail") {
		t.Errorf("Stderr() = %q, want the captured text", got)
	}
	// An empty stream must be a no-op.
	c.drainStderr(strings.NewReader(""))
}

func TestRequestCanceledUnregisters(t *testing.T) {
	c := newFakeClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.request(ctx, "tools/list", nil); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestRPCErrorString(t *testing.T) {
	if got := (&rpcError{Message: "nope"}).Error(); got != "nope" {
		t.Errorf("rpcError.Error() = %q, want nope", got)
	}
}

func TestStdioErrIsSafeAfterClose(t *testing.T) {
	c := newFakeClient(t)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The read loop records EOF once the transport closes; err() must be safe
	// to call regardless of the exact timing.
	_ = c.err()
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func TestStdioTransportReadWriteClose(t *testing.T) {
	var w bytes.Buffer
	tr := &stdioTransport{r: io.NopCloser(strings.NewReader("hello")), w: nopWriteCloser{&w}}

	buf := make([]byte, 4)
	n, err := tr.Read(buf)
	if err != nil || string(buf[:n]) != "hell" {
		t.Fatalf("Read = %q, %v", buf[:n], err)
	}
	if _, err := tr.Write([]byte("world")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if w.String() != "world" {
		t.Errorf("wrote %q, want world", w.String())
	}
	if err := tr.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestNewStdioErrors(t *testing.T) {
	if _, err := NewStdio(ServerConfig{Name: "x"}, ClientInfo{}); err == nil {
		t.Error("a missing command should fail")
	}
	if _, err := NewStdio(ServerConfig{Name: "x", Command: "zcode-no-such-bin-xyz"}, ClientInfo{}); err == nil {
		t.Error("an unstartable command should fail")
	}
}

func TestNewStdioStartsAndCloses(t *testing.T) {
	c, err := NewStdio(ServerConfig{Name: "x", Command: "cmd", Args: []string{"/c", "exit"}}, ClientInfo{Name: "t"})
	if err != nil {
		t.Skipf("cannot start cmd: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
