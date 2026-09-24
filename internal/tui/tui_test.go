package tui

import (
	"context"
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
