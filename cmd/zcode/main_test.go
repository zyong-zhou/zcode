package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunOneShot drives the whole program against a fake OpenAI-compatible
// server: a tool call, then a final answer written to -out. It exercises the
// config, agent, tools, and output wiring together.
func TestRunOneShot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("你好世界"), 0o644); err != nil {
		t.Fatal(err)
	}

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"hello.txt\"}"}}]}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"# 登录设计\n完成"}}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`)
	}))
	defer srv.Close()

	cfgPath := filepath.Join(dir, "config.json")
	cfg := fmt.Sprintf(`{"base_url":%q,"api_key":"test","model":"fake","max_steps":4,"memory":{"enabled":false},"rules":{"enabled":false},"session":{"enabled":false}}`, srv.URL)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "design.md")
	restore := os.Args
	defer func() { os.Args = restore }()
	os.Args = []string{"zcode", "-request", "设计一个登录系统", "-dir", dir, "-config", cfgPath, "-out", out, "-mode", "build"}

	if err := run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if calls < 2 {
		t.Errorf("model called %d times, want at least 2 (tool then answer)", calls)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(string(got), "登录设计") {
		t.Errorf("output = %q", got)
	}
}

func TestRunResumeRequiresSessions(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfg := `{"api_key":"test","model":"fake","memory":{"enabled":false},"rules":{"enabled":false},"session":{"enabled":false}}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	restore := os.Args
	defer func() { os.Args = restore }()
	os.Args = []string{"zcode", "-request", "x", "-dir", dir, "-config", cfgPath, "-continue"}

	err := run()
	if err == nil || !strings.Contains(err.Error(), "session") {
		t.Fatalf("err = %v, want a session-related error", err)
	}
}

func TestRunUnknownSession(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfg := `{"api_key":"test","model":"fake","memory":{"enabled":false},"rules":{"enabled":false},"session":{"enabled":true}}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	restore := os.Args
	defer func() { os.Args = restore }()
	os.Args = []string{"zcode", "-request", "x", "-dir", dir, "-config", cfgPath, "-session", "does-not-exist"}

	err := run()
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want a not-found error", err)
	}
}

func TestToolRootExplicitDir(t *testing.T) {
	dir := t.TempDir()
	got, err := toolRoot(dir)
	if err != nil {
		t.Fatalf("toolRoot: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("toolRoot(%q) = %q, want an absolute path", dir, got)
	}
}

func TestIsGoRunDir(t *testing.T) {
	goRun := filepath.Join("C:", "Users", "x", "AppData", "Local", "Temp", "go-build123", "b001", "exe")
	if !isGoRunDir(goRun) {
		t.Errorf("isGoRunDir(%q) = false, want true", goRun)
	}
	normal := filepath.Join("C:", "work", "zcode", "bin")
	if isGoRunDir(normal) {
		t.Errorf("isGoRunDir(%q) = true, want false", normal)
	}
}
