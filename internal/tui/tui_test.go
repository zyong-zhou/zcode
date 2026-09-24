package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/zhouyong/zcode/internal/backup"
	"github.com/zhouyong/zcode/internal/command"
	"github.com/zhouyong/zcode/internal/llm"
	"github.com/zhouyong/zcode/internal/memory"
	"github.com/zhouyong/zcode/internal/session"
	"github.com/zhouyong/zcode/internal/skill"
	"github.com/zhouyong/zcode/internal/tools"
	"github.com/zhouyong/zcode/internal/zcode"
)

type fakeClient struct{ reply string }

func (f fakeClient) Chat(_ context.Context, _ llm.Request) (llm.Response, error) {
	return llm.Response{Content: f.reply}, nil
}

func newTestModel() *model {
	ag := zcode.New(fakeClient{reply: "设计如下"}, tools.NewRegistry(), zcode.Options{System: "sys"})
	m := newModel(ag, Options{Root: ".", Model: "test-model"})
	// Size the window so layout/refresh are active.
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return m
}

func TestModelSubmitAndAnswer(t *testing.T) {
	m := newTestModel()

	m.input.SetValue("做一个登录系统")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a command to run the agent")
	}
	if !m.busy {
		t.Fatal("model should be busy after submit")
	}
	if v := m.View(); !strings.Contains(v, "做一个登录系统") {
		t.Errorf("view missing user input:\n%s", v)
	}

	// A streamed fragment, the assistant event, then the final result. The
	// answer must appear exactly once.
	_, _ = m.Update(deltaMsg("设计如下"))
	_, _ = m.Update(eventMsg(zcode.Event{Kind: zcode.EventAssistant, Step: 1, Content: "设计如下"}))
	_, _ = m.Update(doneMsg{answer: "设计如下"})

	if m.busy {
		t.Error("model still busy after done")
	}
	v := m.View()
	if n := strings.Count(v, "设计如下"); n != 1 {
		t.Errorf("answer rendered %d times, want 1:\n%s", n, v)
	}
}

func TestModelSlashHelp(t *testing.T) {
	m := newTestModel()
	m.input.SetValue("/help")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	// Inspect the help block directly: the viewport scrolls to its tail, so the
	// top of a long help body is not visible.
	body := m.blocks[len(m.blocks)-1].text
	for _, want := range []string{"/reset", "/cost", "/context", "/compact", "/exit"} {
		if !strings.Contains(body, want) {
			t.Errorf("help output missing %q:\n%s", want, body)
		}
	}
}

func TestModelCustomCommandAndSkills(t *testing.T) {
	ag := zcode.New(fakeClient{reply: "ok"}, tools.NewRegistry(), zcode.Options{System: "sys"})
	m := newModel(ag, Options{
		Root:  ".",
		Model: "test-model",
		Commands: map[string]command.Command{
			"design": {Name: "design", Description: "生成设计", Body: "请设计 $ARGUMENTS"},
		},
		Skills: []skill.Skill{{Name: "release", Description: "发布流程", Body: "步骤"}},
	})
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	m.input.SetValue("/skills")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if v := m.View(); !strings.Contains(v, "release") {
		t.Errorf("/skills output missing the skill:\n%s", v)
	}

	m.input.SetValue("/design 登录")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || !m.busy {
		t.Fatalf("custom command did not submit: cmd=%v busy=%v", cmd, m.busy)
	}
	if v := m.View(); !strings.Contains(v, "/design 登录") {
		t.Errorf("custom command not echoed:\n%s", v)
	}

	_, _ = m.Update(doneMsg{answer: "ok"})
	m.input.SetValue("/help")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if v := m.View(); !strings.Contains(v, "/design") {
		t.Errorf("/help should list custom commands:\n%s", v)
	}
}

func TestModelPermissionPrompt(t *testing.T) {
	m := newTestModel()
	reply := make(chan bool, 1)
	m.permReq = &zcode.PermissionRequest{Tool: "run_command", Args: "{}"}
	m.permReply = reply

	if v := m.View(); !strings.Contains(v, "run_command") {
		t.Errorf("prompt not rendered:\n%s", v)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if got := <-reply; !got {
		t.Error("y should approve")
	}
	if m.permReq != nil {
		t.Error("prompt not cleared after answering")
	}
}

func TestModelPermissionDeny(t *testing.T) {
	m := newTestModel()
	reply := make(chan bool, 1)
	m.permReq = &zcode.PermissionRequest{Tool: "run_command"}
	m.permReply = reply

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if got := <-reply; got {
		t.Error("n should deny")
	}
}

func TestModelToolActivity(t *testing.T) {
	m := newTestModel()
	_, _ = m.Update(eventMsg(zcode.Event{Kind: zcode.EventToolCall, Step: 1, Tool: "list_dir", Args: `{"path":"."}`}))
	if v := m.View(); !strings.Contains(v, "list_dir") {
		t.Errorf("view missing tool call:\n%s", v)
	}
}

func TestModelInputIsMultiLine(t *testing.T) {
	m := newTestModel()
	if got := m.input.Height(); got < inputMinHeight {
		t.Errorf("input height = %d, want >= %d", got, inputMinHeight)
	}

	m.input.SetValue("第一行")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m.input.InsertString("第二行")

	if v := m.input.Value(); !strings.Contains(v, "\n") {
		t.Errorf("Ctrl+J did not insert a newline: %q", v)
	}
	if got := m.input.LineCount(); got != 2 {
		t.Errorf("LineCount = %d, want 2", got)
	}
}

func TestViewFillsHeight(t *testing.T) {
	m := newTestModel() // 80x24
	if got := len(strings.Split(m.View(), "\n")); got != 24 {
		t.Errorf("view lines = %d, want 24", got)
	}
}

func TestModelIsInBottomStatus(t *testing.T) {
	m := newTestModel()
	lines := strings.Split(m.View(), "\n")
	status := lines[len(lines)-1]
	if !strings.Contains(status, "test-model") {
		t.Errorf("bottom status missing model: %q", status)
	}
	if strings.Contains(status, "zcode") {
		t.Errorf("bottom status should not show the product name: %q", status)
	}
	if strings.Contains(lines[0], "test-model") {
		t.Errorf("top line should not contain model info: %q", lines[0])
	}
}

func newTestModelWithStore(t *testing.T) *model {
	t.Helper()
	st, err := memory.Open(filepath.Join(t.TempDir(), ".zcode", "memory.md"))
	if err != nil {
		t.Fatalf("memory.Open: %v", err)
	}
	ag := zcode.New(fakeClient{reply: "ok"}, tools.NewRegistry(), zcode.Options{System: "sys"})
	m := newModel(ag, Options{Root: ".", Model: "test-model", Store: st})
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return m
}

func TestModelSlashModeCommands(t *testing.T) {
	m := newTestModel()
	m.command("/plan")
	if m.ag.Mode != zcode.ModePlan {
		t.Fatalf("mode = %q, want plan", m.ag.Mode)
	}
	m.command("/build")
	if m.ag.Mode != zcode.ModeBuild {
		t.Fatalf("mode = %q, want build", m.ag.Mode)
	}
	m.command("/mode")
	if v := m.View(); !strings.Contains(v, "当前模式：build") {
		t.Errorf("view missing mode line:\n%s", v)
	}
}

func TestModelMemoryCommand(t *testing.T) {
	m := newTestModel()
	m.command("/memory")
	if v := m.View(); !strings.Contains(v, "记忆未启用") {
		t.Errorf("nil store should warn:\n%s", v)
	}

	m = newTestModelWithStore(t)
	m.command("/memory add 项目用 gofmt")
	if v := m.View(); !strings.Contains(v, "已记住") {
		t.Errorf("add should confirm:\n%s", v)
	}
	m.command("/memory add 项目用 gofmt")
	if v := m.View(); !strings.Contains(v, "已存在相同记忆") {
		t.Errorf("duplicate should be reported:\n%s", v)
	}
	m.command("/memory")
	if v := m.View(); !strings.Contains(v, "项目记忆（1 条）") {
		t.Errorf("list should show the count:\n%s", v)
	}
	m.command("/memory clear")
	if v := m.View(); !strings.Contains(v, "已清空 1 条记忆") {
		t.Errorf("clear should report the count:\n%s", v)
	}
}

func TestModelToolResultRendering(t *testing.T) {
	m := newTestModel()
	_, _ = m.Update(eventMsg(zcode.Event{Kind: zcode.EventToolResult, Step: 1, Tool: "read_file", Result: "abc"}))
	if v := m.View(); !strings.Contains(v, "← read_file") || !strings.Contains(v, "ok") {
		t.Errorf("success result missing:\n%s", v)
	}
	_, _ = m.Update(eventMsg(zcode.Event{Kind: zcode.EventToolResult, Step: 1, Tool: "write_file", Result: "", Err: "denied"}))
	if v := m.View(); !strings.Contains(v, "error:") || !strings.Contains(v, "denied") {
		t.Errorf("error result missing:\n%s", v)
	}
}

func TestModelEscCancels(t *testing.T) {
	m := newTestModel()
	m.input.SetValue("做一个系统")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.busy {
		t.Fatal("expected busy after submit")
	}
	cancelled := false
	m.cancel = func() { cancelled = true }
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !cancelled {
		t.Error("Esc did not call cancel")
	}
	lines := strings.Split(m.View(), "\n")
	if status := lines[len(lines)-1]; !strings.Contains(status, "正在取消") {
		t.Errorf("status = %q, want a cancelling notice", status)
	}
}

func TestModelResetAndClear(t *testing.T) {
	m := newTestModel()
	m.input.SetValue("需求一")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_, _ = m.Update(doneMsg{answer: "答"})
	if len(m.blocks) == 0 {
		t.Fatal("expected some blocks after a turn")
	}

	m.command("/reset")
	if len(m.blocks) != 0 {
		t.Errorf("reset left %d blocks", len(m.blocks))
	}
	if v := m.View(); !strings.Contains(v, "已清空上下文") {
		t.Errorf("reset status missing:\n%s", v)
	}

	m.blocks = append(m.blocks, block{role: roleSystem, text: "x"})
	m.command("/clear")
	if len(m.blocks) != 0 {
		t.Errorf("clear left %d blocks", len(m.blocks))
	}
}

func TestModelTogglesMode(t *testing.T) {
	m := newTestModel()
	if m.ag.Mode != zcode.ModeBuild {
		t.Fatalf("default mode = %q, want build", m.ag.Mode)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.ag.Mode != zcode.ModePlan {
		t.Fatalf("mode after toggle = %q, want plan", m.ag.Mode)
	}
	if v := m.View(); !strings.Contains(v, "PLAN") {
		t.Errorf("view missing PLAN badge:\n%s", v)
	}
	if got := len(strings.Split(m.View(), "\n")); got != 24 {
		t.Errorf("view lines = %d, want 24", got)
	}
}

func TestModelPermissionPreviewShown(t *testing.T) {
	m := newTestModel()
	m.permReq = &zcode.PermissionRequest{
		Tool:    "write_file",
		Args:    "{}",
		Preview: "写入 a.txt（5 字节）:\nhello",
	}
	m.permReply = make(chan bool, 1)
	if v := m.View(); !strings.Contains(v, "write_file") || !strings.Contains(v, "hello") {
		t.Errorf("permission preview not rendered:\n%s", v)
	}
}

func TestModelCostCommand(t *testing.T) {
	m := newTestModel()
	m.ag.Usage = llm.Usage{PromptTokens: 12, CompletionTokens: 3, TotalTokens: 15}
	m.command("/cost")
	if v := m.View(); !strings.Contains(v, "prompt 12") || !strings.Contains(v, "total 15") {
		t.Errorf("/cost missing usage:\n%s", v)
	}
}

func TestModelContextCommand(t *testing.T) {
	ag := zcode.New(fakeClient{reply: "ok"}, tools.NewRegistry(), zcode.Options{System: "sys"})
	m := newModel(ag, Options{
		Root:           ".",
		Model:          "test-model",
		EstimateTokens: func([]llm.Message) int { return 100 },
		ContextLimit:   1000,
	})
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.command("/context")
	if v := m.View(); !strings.Contains(v, "100 tokens") || !strings.Contains(v, "1000") {
		t.Errorf("/context missing estimate:\n%s", v)
	}
}

func TestModelCompactCommand(t *testing.T) {
	ag := zcode.New(fakeClient{reply: "ok"}, tools.NewRegistry(), zcode.Options{System: "sys"})
	m := newModel(ag, Options{Root: ".", Model: "test-model"})
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	m.command("/compact")
	if v := m.View(); !strings.Contains(v, "未启用上下文压缩") {
		t.Errorf("/compact without a compactor should warn:\n%s", v)
	}

	ag.Compactor = fakeCompactor{changed: true}
	cmd := m.command("/compact")
	if cmd == nil || !m.busy {
		t.Fatalf("compact did not start: cmd=%v busy=%v", cmd, m.busy)
	}
	_, _ = m.Update(compactMsg{changed: true})
	if v := m.View(); !strings.Contains(v, "已压缩上下文") {
		t.Errorf("compact result missing:\n%s", v)
	}
}

// fakeCompactor reports a fixed changed flag.
type fakeCompactor struct{ changed bool }

func (f fakeCompactor) Compact(_ context.Context, msgs []llm.Message) ([]llm.Message, bool, error) {
	return msgs, f.changed, nil
}

func TestModelTurnElapsedShown(t *testing.T) {
	m := newTestModel()
	m.input.SetValue("需求")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.turnStart.IsZero() {
		t.Fatal("turnStart not set on submit")
	}
	_, _ = m.Update(doneMsg{answer: "答"})
	if !m.turnStart.IsZero() {
		t.Error("turnStart not cleared after finish")
	}

	m.lastCost = 1500 * time.Millisecond
	if v := m.View(); !strings.Contains(v, "1.5s") {
		t.Errorf("status line missing the duration:\n%s", v)
	}
}

func TestModelBackupCommands(t *testing.T) {
	m := newTestModel()
	m.command("/undo")
	if v := m.View(); !strings.Contains(v, "未启用备份") {
		t.Errorf("/undo without a store should warn:\n%s", v)
	}

	root := t.TempDir()
	src := filepath.Join(root, "a.txt")
	if err := os.WriteFile(src, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := &backup.Store{Dir: filepath.Join(root, "b"), Root: root}
	if _, err := st.Snapshot("a.txt", src); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}

	ag := zcode.New(fakeClient{reply: "ok"}, tools.NewRegistry(), zcode.Options{System: "sys"})
	m2 := newModel(ag, Options{Root: root, Model: "test-model", Backups: st})
	_, _ = m2.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	m2.command("/backups")
	if v := m2.View(); !strings.Contains(v, "a.txt") {
		t.Errorf("/backups missing the source:\n%s", v)
	}
	m2.command("/undo")
	if v := m2.View(); !strings.Contains(v, "已撤销：a.txt") {
		t.Errorf("/undo did not confirm:\n%s", v)
	}
	if got, _ := os.ReadFile(src); string(got) != "v1" {
		t.Errorf("content after undo = %q, want v1", got)
	}
}

func TestModelUndoWalksBackAndIndexes(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "a.txt")
	if err := os.WriteFile(src, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := &backup.Store{Dir: filepath.Join(root, "b"), Root: root}
	if _, err := st.Snapshot("a.txt", src); err != nil { // holds v1
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Snapshot("a.txt", src); err != nil { // holds v2
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("v3"), 0o644); err != nil {
		t.Fatal(err)
	}

	ag := zcode.New(fakeClient{reply: "ok"}, tools.NewRegistry(), zcode.Options{System: "sys"})
	m := newModel(ag, Options{Root: root, Model: "test-model", Backups: st})
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	// /undo consumes the newest snapshot (v2) so a second /undo reaches v1.
	m.command("/undo")
	if got, _ := os.ReadFile(src); string(got) != "v2" {
		t.Fatalf("after first undo content = %q, want v2", got)
	}
	m.command("/undo")
	if got, _ := os.ReadFile(src); string(got) != "v1" {
		t.Fatalf("after second undo content = %q, want v1", got)
	}
	// Both snapshots are consumed; a third undo has nothing to do.
	m.command("/undo")
	if v := m.View(); !strings.Contains(v, "没有可撤销的备份") {
		t.Errorf("expected an empty-stack notice:\n%s", v)
	}

	// An explicit index selects a snapshot without popping the newest first.
	if err := os.WriteFile(src, []byte("v4"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Snapshot("a.txt", src); err != nil { // holds v4 (newest)
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("v5"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Snapshot("a.txt", src); err != nil { // holds v5 (newest)
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("v6"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.command("/undo 2") // second-newest = the v4 snapshot
	if got, _ := os.ReadFile(src); string(got) != "v4" {
		t.Errorf("indexed undo content = %q, want v4", got)
	}
	m.command("/undo 9")
	if v := m.View(); !strings.Contains(v, "超出范围") {
		t.Errorf("out-of-range index should error:\n%s", v)
	}
}

func TestModelModelCommand(t *testing.T) {
	m := newTestModel()
	m.command("/model")
	if v := m.View(); !strings.Contains(v, "test-model") {
		t.Errorf("/model should show the current model:\n%s", v)
	}
	m.command("/model other")
	if v := m.View(); !strings.Contains(v, "不支持运行时切换") {
		t.Errorf("expected a no-switcher notice:\n%s", v)
	}

	ag := zcode.New(fakeClient{reply: "ok"}, tools.NewRegistry(), zcode.Options{System: "sys"})
	var switched string
	m2 := newModel(ag, Options{
		Root: ".", Model: "test-model",
		SwitchModel: func(name string) (string, error) {
			switched = name
			return name, nil
		},
	})
	_, _ = m2.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2.command("/model deepseek")
	if switched != "deepseek" {
		t.Errorf("SwitchModel got %q, want deepseek", switched)
	}
	if m2.opts.Model != "deepseek" {
		t.Errorf("opts.Model = %q, want deepseek", m2.opts.Model)
	}
	if v := m2.View(); !strings.Contains(v, "已切换模型：deepseek") {
		t.Errorf("switch confirmation missing:\n%s", v)
	}

	m2.opts.SwitchModel = func(string) (string, error) { return "", errors.New("boom") }
	m2.command("/model x")
	if v := m2.View(); !strings.Contains(v, "切换模型失败：boom") {
		t.Errorf("switch error not surfaced:\n%s", v)
	}
}

func TestModelMCPCommand(t *testing.T) {
	m := newTestModel()
	m.command("/mcp")
	if v := m.View(); !strings.Contains(v, "未启用 MCP") {
		t.Errorf("/mcp without a status should warn:\n%s", v)
	}

	ag := zcode.New(fakeClient{reply: "ok"}, tools.NewRegistry(), zcode.Options{System: "sys"})
	m2 := newModel(ag, Options{
		Root: ".", Model: "test-model",
		MCPStatus: func() string { return "MCP 服务器\n  demo  2 个工具" },
	})
	_, _ = m2.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m2.command("/mcp")
	if v := m2.View(); !strings.Contains(v, "demo") {
		t.Errorf("/mcp missing server info:\n%s", v)
	}
}

func TestModelSessionCommandAndNew(t *testing.T) {
	mgr := session.NewManager(session.Store{Dir: t.TempDir()})
	defer mgr.Close()
	ag := zcode.New(fakeClient{reply: "ok"}, tools.NewRegistry(), zcode.Options{System: "sys"})
	ag.Transcript = mgr
	m := newModel(ag, Options{Root: ".", Model: "test-model", Sessions: mgr})
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	m.command("/session")
	if v := m.View(); !strings.Contains(v, "当前没有活动会话") {
		t.Errorf("/session with no active session:\n%s", v)
	}
	m.command("/new")
	if v := m.View(); !strings.Contains(v, "新会话") {
		t.Errorf("/new did not report the new session:\n%s", v)
	}
	m.command("/session")
	if v := m.View(); !strings.Contains(v, "当前会话") {
		t.Errorf("/session did not report the active session:\n%s", v)
	}
}

func TestModelSessionCommandErrors(t *testing.T) {
	mgr := session.NewManager(session.Store{Dir: t.TempDir()})
	defer mgr.Close()
	ag := zcode.New(fakeClient{reply: "ok"}, tools.NewRegistry(), zcode.Options{System: "sys"})
	ag.Transcript = mgr
	m := newModel(ag, Options{Root: ".", Model: "test-model", Sessions: mgr})
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	for _, tc := range []struct{ cmd, want string }{
		{"/delete", "用法：/delete"},
		{"/rename", "用法：/rename"},
		{"/resume", "用法：/resume"},
		{"/resume 99", "超出范围"},
		{"/delete missing", "删除会话失败"},
		{"/rename missing new", "重命名会话失败"},
		{"/resume missing", "恢复会话失败"},
	} {
		m.command(tc.cmd)
		if v := m.View(); !strings.Contains(v, tc.want) {
			t.Errorf("%s: view missing %q:\n%s", tc.cmd, tc.want, v)
		}
	}
}

func TestModelHandleEventKinds(t *testing.T) {
	m := newTestModel()
	for _, e := range []zcode.Event{
		{Kind: zcode.EventToolCall, Tool: "read_file", Args: `{"path":"a"}`},
		{Kind: zcode.EventToolResult, Tool: "read_file", Result: "hello"},
		{Kind: zcode.EventThought, Content: "思考中"},
		{Kind: zcode.EventPermission, Tool: "run_command", Result: "deny"},
		{Kind: zcode.EventCompact, Result: "10 → 5"},
		{Kind: zcode.EventWarning, Err: "小心"},
		{Kind: zcode.EventLimit, Err: "达到上限"},
	} {
		_, _ = m.Update(eventMsg(e))
	}
	v := m.View()
	for _, want := range []string{"read_file", "权限 run_command", "已压缩上下文", "小心", "达到上限", "思考中"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
}

func TestModelBackupsEmpty(t *testing.T) {
	root := t.TempDir()
	st := &backup.Store{Dir: filepath.Join(root, "b"), Root: root}
	ag := zcode.New(fakeClient{reply: "ok"}, tools.NewRegistry(), zcode.Options{System: "sys"})
	m := newModel(ag, Options{Root: root, Model: "test-model", Backups: st})
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	m.command("/backups")
	if v := m.View(); !strings.Contains(v, "暂无备份") {
		t.Errorf("/backups with no snapshots:\n%s", v)
	}
	m.command("/undo")
	if v := m.View(); !strings.Contains(v, "没有可撤销") {
		t.Errorf("/undo with no snapshots:\n%s", v)
	}
}

func TestModelToggleModeFromPlan(t *testing.T) {
	m := newTestModel()
	m.ag.SetMode(zcode.ModePlan)
	m.toggleMode()
	if m.ag.Mode != zcode.ModeBuild {
		t.Errorf("toggle from plan = %q, want build", m.ag.Mode)
	}
	m.toggleMode()
	if m.ag.Mode != zcode.ModePlan {
		t.Errorf("toggle from build = %q, want plan", m.ag.Mode)
	}
}

func TestModelSessionManagement(t *testing.T) {
	dir := t.TempDir()
	mgr := session.NewManager(session.Store{Dir: dir})
	defer mgr.Close()

	ag := zcode.New(fakeClient{reply: "ok"}, tools.NewRegistry(), zcode.Options{System: "sys"})
	ag.Transcript = mgr
	m := newModel(ag, Options{Root: dir, Model: "test-model", Sessions: mgr})
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	first, err := mgr.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Append(llm.Message{Role: "user", Content: "第一个"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := mgr.New(); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Append(llm.Message{Role: "user", Content: "第二个"}); err != nil {
		t.Fatal(err)
	}

	m.command("/sessions")
	if v := m.View(); !strings.Contains(v, "1.") || !strings.Contains(v, "2.") {
		t.Errorf("/sessions should number entries:\n%s", v)
	}

	// Index 1 is the newest session.
	m.command("/rename 1 renamed")
	if v := m.View(); !strings.Contains(v, "已重命名会话") {
		t.Errorf("rename by index failed:\n%s", v)
	}
	m.command("/resume 1")
	if v := m.View(); !strings.Contains(v, "已恢复会话：renamed") {
		t.Errorf("resume by index failed:\n%s", v)
	}
	m.command("/delete " + first)
	if v := m.View(); !strings.Contains(v, "已删除会话：") {
		t.Errorf("delete by id failed:\n%s", v)
	}
	m.command("/delete 99")
	if v := m.View(); !strings.Contains(v, "超出范围") {
		t.Errorf("out-of-range index should error:\n%s", v)
	}
}

func TestModelSessionListAndNewErrors(t *testing.T) {
	// A regular file where the session directory should be makes New fail,
	// exercising its error branch.
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := session.NewManager(session.Store{Dir: file})
	defer mgr.Close()

	ag := zcode.New(fakeClient{reply: "ok"}, tools.NewRegistry(), zcode.Options{System: "sys"})
	m := newModel(ag, Options{Root: ".", Model: "test-model", Sessions: mgr})
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	m.command("/new")
	if v := m.View(); !strings.Contains(v, "新建会话失败") {
		t.Errorf("/new error branch missing:\n%s", v)
	}
	// Listing a non-directory yields no sessions (the path is treated as empty).
	m.command("/sessions")
	if v := m.View(); !strings.Contains(v, "没有历史会话") {
		t.Errorf("/sessions empty branch missing:\n%s", v)
	}
}

func TestModelReplayTranscript(t *testing.T) {
	mgr := session.NewManager(session.Store{Dir: t.TempDir()})
	defer mgr.Close()
	if _, err := mgr.New(); err != nil {
		t.Fatal(err)
	}
	for _, msg := range []llm.Message{
		{Role: "user", Content: "问题"},
		{Role: "assistant", Content: "回答"},
		{Role: "tool", Content: "ignored"},
	} {
		if err := mgr.Append(msg); err != nil {
			t.Fatal(err)
		}
	}

	ag := zcode.New(fakeClient{reply: "ok"}, tools.NewRegistry(), zcode.Options{System: "sys"})
	m := newModel(ag, Options{Root: ".", Model: "test-model", Sessions: mgr})
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.replayTranscript()
	m.layout()

	v := m.View()
	if !strings.Contains(v, "问题") || !strings.Contains(v, "回答") {
		t.Errorf("replay missing restored user/assistant messages:\n%s", v)
	}
	if strings.Contains(v, "ignored") {
		t.Errorf("replay rendered a non user/assistant message:\n%s", v)
	}
}

func TestModelActivity(t *testing.T) {
	m := newTestModel()
	if got := m.activity(); !strings.Contains(got, "思考") {
		t.Errorf("activity with no tool = %q, want 思考中", got)
	}
	m.lastTool = "read_file"
	if got := m.activity(); !strings.Contains(got, "read_file") {
		t.Errorf("activity with a tool = %q, want the tool name", got)
	}
}

func TestModelKeyBranches(t *testing.T) {
	m := newTestModel()

	// Ctrl+C returns the quit command.
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd == nil {
		t.Error("Ctrl+C should return tea.Quit")
	}
	// Page keys scroll the viewport.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	// Ctrl+J inserts a newline instead of submitting.
	m.input.SetValue("line")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if !strings.Contains(m.input.Value(), "\n") {
		t.Errorf("Ctrl+J should insert a newline, got %q", m.input.Value())
	}
	// Enter on empty input does nothing.
	m.input.Reset()
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.busy {
		t.Error("Enter with empty input should not start a run")
	}
	// Mouse messages are forwarded to the viewport.
	_, _ = m.Update(tea.MouseMsg{})
}

func TestModelBackupsTruncatesLongList(t *testing.T) {
	root := t.TempDir()
	st := &backup.Store{Dir: filepath.Join(root, "b"), Root: root}
	src := filepath.Join(root, "a.txt")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		if _, err := st.Snapshot("a.txt", src); err != nil {
			t.Fatal(err)
		}
	}

	ag := zcode.New(fakeClient{reply: "ok"}, tools.NewRegistry(), zcode.Options{System: "sys"})
	m := newModel(ag, Options{Root: root, Model: "test-model", Backups: st})
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.command("/backups")
	if v := m.View(); !strings.Contains(v, "还有") {
		t.Errorf("/backups should truncate a long list:\n%s", v)
	}
}

func TestCollapsedPaste(t *testing.T) {
	got := collapsedPaste("第一行\n第二行\n第三行", 80)
	for _, want := range []string{"第一行", "第三行", "....", "共 3 行"} {
		if !strings.Contains(got, want) {
			t.Errorf("collapsedPaste missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "第二行") {
		t.Errorf("collapsedPaste should elide the middle: %q", got)
	}

	// A single line passes through unchanged.
	if got := collapsedPaste("只有一行", 80); got != "只有一行" {
		t.Errorf("single line = %q, want 只有一行", got)
	}

	// A narrow box keeps the head, the separator, and the tail without
	// overflowing the column budget.
	narrow := collapsedPaste(strings.Repeat("前", 30)+"\n"+strings.Repeat("后", 30), 40)
	if !strings.Contains(narrow, "....") {
		t.Errorf("narrow paste lost the separator: %q", narrow)
	}
	if strings.Count(narrow, "前") > 15 || strings.Count(narrow, "后") > 15 {
		t.Errorf("narrow paste was not truncated: %q", narrow)
	}
}

func TestPasteText(t *testing.T) {
	if _, multi := pasteText([]rune("hello")); multi {
		t.Error("a single-line paste should not be multi-line")
	}
	if _, multi := pasteText(nil); multi {
		t.Error("an empty paste should not be multi-line")
	}
	text, multi := pasteText([]rune("a\r\nb"))
	if !multi || text != "a\nb" {
		t.Errorf("pasteText(CRLF) = %q, %v; want a\\nb, true", text, multi)
	}
}

func TestModelPasteCollapsesMultiLine(t *testing.T) {
	m := newTestModel()
	full := "第一行\n第二行\n第三行\n第四行"
	// Bracketed paste: one message carrying the whole block.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(full), Paste: true})

	if !m.pasteCollapsed {
		t.Fatal("a multi-line paste should collapse the input")
	}
	v := m.View()
	if !strings.Contains(v, "第一行 .... 第四行") {
		t.Errorf("collapsed summary missing:\n%s", v)
	}
	if strings.Contains(v, "第二行") {
		t.Errorf("collapsed summary should hide middle lines:\n%s", v)
	}
	if !strings.Contains(v, "共 4 行") {
		t.Errorf("collapsed summary should show the line count:\n%s", v)
	}
	if got := m.input.Value(); got != full {
		t.Errorf("full pasted text was not preserved: %q", got)
	}
	if h := m.input.Height(); h != inputMinHeight {
		t.Errorf("collapsed input height = %d, want the fixed %d", h, inputMinHeight)
	}

	// A real Enter comes after a pause and sends the full multi-line text.
	m.lastInputAt = time.Now().Add(-time.Second)
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.blocks) == 0 {
		t.Fatal("Enter should start a request")
	}
	last := m.blocks[len(m.blocks)-1]
	if last.role != roleUser || last.text != full {
		t.Errorf("submitted block = %+v, want the full pasted text", last)
	}
}

func TestModelWindowsPasteCollapses(t *testing.T) {
	// The Windows console reader hands over a paste as single-rune messages;
	// the line break is a plain rune, never KeyMsg.Paste.
	m := newTestModel()
	for _, r := range []string{"第一行", "\n", "第二行", "\n", "第三行"} {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(r)})
	}
	if !m.pasteCollapsed {
		t.Fatal("a Windows-style multi-line paste should collapse the input")
	}
	if got := m.input.Value(); got != "第一行\n第二行\n第三行" {
		t.Errorf("value = %q, want the full three lines", got)
	}
	v := m.View()
	if !strings.Contains(v, "第一行 .... 第三行") {
		t.Errorf("collapsed summary missing:\n%s", v)
	}
	if strings.Contains(v, "第二行") {
		t.Errorf("collapsed summary should hide the middle line:\n%s", v)
	}
}

func TestModelWindowsPastedEnterFolds(t *testing.T) {
	// Some terminals deliver a pasted line break as Enter right after the
	// preceding characters; it must fold into the input, not submit.
	m := newTestModel()
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m.lastInputAt = time.Now()
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})

	if len(m.blocks) != 0 {
		t.Fatalf("a pasted line break must not submit: %+v", m.blocks)
	}
	if !m.pasteCollapsed {
		t.Error("a pasted line break should collapse the input")
	}
	if got := m.input.Value(); got != "a\nb" {
		t.Errorf("value = %q, want a\\nb", got)
	}
}

func TestModelNonRuneKeyRevealsCollapsedPaste(t *testing.T) {
	m := newTestModel()
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a\nb\nc"), Paste: true})
	if !m.pasteCollapsed {
		t.Fatal("expected a collapsed paste")
	}

	// Navigation is an explicit action and reveals the full text again.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if m.pasteCollapsed {
		t.Error("a cursor move should reveal the full text")
	}
	if v := m.View(); !strings.Contains(v, "b") {
		t.Errorf("moving should show the full text:\n%s", v)
	}
}

func TestModelTypingKeepsCollapsedPaste(t *testing.T) {
	m := newTestModel()
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("第一行\n第二行"), Paste: true})
	// A rune insert may still be the tail of the paste, so the summary stays
	// and simply grows to include the typed text.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("请实现")})
	if !m.pasteCollapsed {
		t.Error("typing right after a paste should keep the summary")
	}
	if got := m.input.Value(); !strings.Contains(got, "请实现") {
		t.Errorf("typed text was lost: %q", got)
	}
	if v := m.View(); !strings.Contains(v, "请实现") {
		t.Errorf("the summary should show the typed text:\n%s", v)
	}
}

func TestModelCtrlJRevealsCollapsedPaste(t *testing.T) {
	m := newTestModel()
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x\ny"), Paste: true})
	if !m.pasteCollapsed {
		t.Fatal("expected a collapsed paste")
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.pasteCollapsed {
		t.Error("Ctrl+J is an edit and should reveal the full text")
	}
}

func TestModelSingleLinePasteDoesNotCollapse(t *testing.T) {
	m := newTestModel()
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello world"), Paste: true})
	if m.pasteCollapsed {
		t.Error("a single-line paste should not collapse")
	}
	if got := m.input.Value(); got != "hello world" {
		t.Errorf("value = %q, want hello world", got)
	}
}

func TestModelInputHeightStaysFixed(t *testing.T) {
	m := newTestModel()
	base := m.input.Height()

	// Many manual lines must not grow the box.
	for i := 0; i < 10; i++ {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	}
	if got := m.input.Height(); got != base {
		t.Errorf("input height after 10 lines = %d, want the fixed %d", got, base)
	}

	// A collapsed paste must not shrink it either.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a\nb\nc\nd"), Paste: true})
	if got := m.input.Height(); got != base {
		t.Errorf("input height while collapsed = %d, want the fixed %d", got, base)
	}

	// The rendered view keeps the same height whether or not it is collapsed.
	lines := func() int { return len(strings.Split(m.View(), "\n")) }
	m.pasteCollapsed = false
	uncollapsed := lines()
	m.pasteCollapsed = true
	collapsed := lines()
	if uncollapsed != collapsed {
		t.Errorf("view height changed on collapse: %d -> %d", uncollapsed, collapsed)
	}
}
