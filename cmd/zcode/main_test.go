package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhouyong/zcode/internal/llm"
	"github.com/zhouyong/zcode/internal/session"
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

// testRequest is the subset of the wire request the tests inspect.
type testRequest struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}

func decodeTestRequest(t *testing.T, r *http.Request) testRequest {
	t.Helper()
	var req testRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Errorf("decode request: %v", err)
	}
	return req
}

func setArgs(t *testing.T, args ...string) {
	t.Helper()
	restore := os.Args
	t.Cleanup(func() { os.Args = restore })
	os.Args = append([]string{"zcode"}, args...)
}

func hasTool(tools []string, want string) bool {
	for _, name := range tools {
		if name == want {
			return true
		}
	}
	return false
}

func TestLoadConfigPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"model":"file-model","max_steps":7}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_MODEL", "env-model")

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Model != "env-model" {
		t.Errorf("model = %q, want env-model (env overrides file)", cfg.Model)
	}
	if cfg.MaxSteps != 7 {
		t.Errorf("max_steps = %d, want 7 from the file", cfg.MaxSteps)
	}

	if _, err := loadConfig(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("an explicit missing config should error")
	}
}

func TestRunPlanModeByDefault(t *testing.T) {
	dir := t.TempDir()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeTestRequest(t, r)
		for _, tool := range req.Tools {
			seen = append(seen, tool.Function.Name)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"方案"}}]}`)
	}))
	defer srv.Close()

	cfg := fmt.Sprintf(`{"base_url":%q,"api_key":"test","model":"fake","memory":{"enabled":false},"rules":{"enabled":false},"session":{"enabled":false}}`, srv.URL)
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	// No -mode: a one-shot run defaults to plan, which hides write tools.
	setArgs(t, "-request", "设计", "-dir", dir, "-config", cfgPath, "-out", filepath.Join(dir, "x.md"))
	if err := run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if hasTool(seen, "write_file") || hasTool(seen, "run_command") {
		t.Errorf("plan mode exposed mutating tools: %v", seen)
	}
	if !hasTool(seen, "read_file") {
		t.Errorf("plan mode should expose read tools, got %v", seen)
	}
}

func TestRunContinueResumesSession(t *testing.T) {
	dir := t.TempDir()
	mgr := session.NewManager(session.Store{Dir: filepath.Join(dir, ".zcode", "sessions")})
	if _, err := mgr.New(); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Append(llm.Message{Role: "user", Content: "旧消息"}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Close(); err != nil {
		t.Fatal(err)
	}

	var sawOld bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeTestRequest(t, r)
		for _, m := range req.Messages {
			if m.Role == "user" && strings.Contains(m.Content, "旧消息") {
				sawOld = true
			}
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"继续"}}]}`)
	}))
	defer srv.Close()

	cfg := fmt.Sprintf(`{"base_url":%q,"api_key":"test","model":"fake","memory":{"enabled":false},"rules":{"enabled":false},"session":{"enabled":true}}`, srv.URL)
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	setArgs(t, "-request", "继续", "-dir", dir, "-config", cfgPath, "-continue", "-out", filepath.Join(dir, "x.md"))
	if err := run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !sawOld {
		t.Error("resumed request did not include the stored transcript")
	}
}

func TestRunAutoYesApprovesAsk(t *testing.T) {
	dir := t.TempDir()
	var (
		calls      int
		toolResult string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		req := decodeTestRequest(t, r)
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"1","type":"function","function":{"name":"run_command","arguments":"{\"command\":\"go\",\"args\":[\"version\"]}"}}]}}]}`)
			return
		}
		for _, m := range req.Messages {
			if m.Role == "tool" {
				toolResult = m.Content
			}
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"done"}}]}`)
	}))
	defer srv.Close()

	cfg := fmt.Sprintf(`{"base_url":%q,"api_key":"test","model":"fake","max_steps":4,"memory":{"enabled":false},"rules":{"enabled":false},"session":{"enabled":false},"permissions":{"default":"ask"}}`, srv.URL)
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "x.md")

	// Without -y, an "ask" decision is denied in a non-interactive run.
	calls, toolResult = 0, ""
	setArgs(t, "-request", "跑一下", "-dir", dir, "-config", cfgPath, "-out", out, "-mode", "build")
	if err := run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(toolResult, "permission denied") {
		t.Errorf("without -y tool result = %q, want a denial", toolResult)
	}

	// With -y the command is approved and actually runs.
	calls, toolResult = 0, ""
	setArgs(t, "-request", "跑一下", "-dir", dir, "-config", cfgPath, "-out", out, "-mode", "build", "-y")
	if err := run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(toolResult, "go1.") {
		t.Errorf("with -y tool result = %q, want go version output", toolResult)
	}
}
