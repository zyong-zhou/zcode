// Package tui renders the program-design agent as an interactive terminal UI:
// a scrollable conversation, a streaming reply, and a bottom input box.
package tui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/zhouyong/zcode/internal/backup"
	"github.com/zhouyong/zcode/internal/command"
	"github.com/zhouyong/zcode/internal/llm"
	"github.com/zhouyong/zcode/internal/memory"
	"github.com/zhouyong/zcode/internal/session"
	"github.com/zhouyong/zcode/internal/skill"
	"github.com/zhouyong/zcode/internal/zcode"
)

const (
	// inputMinHeight is the fixed height of the input box in rows. The box
	// never grows or shrinks; longer content scrolls inside it.
	inputMinHeight = 3
	// pasteBurst is the largest gap between two key events that still counts as
	// one paste. The Windows console hands a paste over as a burst of
	// single-rune messages and leaves KeyMsg.Paste false, so a pasted line
	// break is recognized by how quickly it follows the previous rune.
	pasteBurst = 30 * time.Millisecond
)

const helpText = `命令
  /help   显示本帮助
  /reset  清空上下文，重新开始
  /clear  只清屏，保留上下文
  /model  显示当前模型（/model <名字> 切换）
  /cost   查看本次会话累计 token 用量
  /context 查看上下文大小估算与上限
  /compact 立即压缩上下文
  /undo   撤销最近一次文件写入（/undo <序号>）
  /backups 列出最近的写入备份
  /mcp    查看 MCP 服务器与工具
  /plan   切换到 Plan 模式（只读）
  /build  切换到 Build 模式（可读写、可执行）
  /mode   显示当前模式
  /memory 查看项目记忆（/memory add <文本> · /memory clear）
  /rules  查看已注入的项目规则（AGENTS.md）
  /session 查看当前会话
  /new    开始新会话（清空上下文）
  /sessions 列出历史会话
  /resume 恢复会话（/resume <id>）
  /skills 列出可用技能
  /exit   退出

快捷键
  Enter 发送 · Ctrl+J 换行（可多行） · Shift+Tab 切换 Plan/Build · PgUp/PgDn 或滚轮滚动 · Esc 取消 · Ctrl+C 退出`

// Options configures the UI.
type Options struct {
	// Root is the directory tools operate in, shown in the status line.
	Root string
	// Model is the model name shown in the status line and /model.
	Model string
	// Store, when set, backs the /memory command. It may be nil.
	Store *memory.Store
	// Rules is the rendered project-rule text shown by /rules. It may be empty.
	Rules string
	// Sessions, when set, backs /session, /new, /sessions, and /resume. It may
	// be nil when persistence is disabled.
	Sessions *session.Manager
	// Commands are user-defined slash commands loaded from .zcode/commands.
	Commands map[string]command.Command
	// Skills are the loaded skills, listed by /skills.
	Skills []skill.Skill
	// MCPToolCount, when set, reports how many tools come from MCP servers. It is
	// called on each render so the count stays accurate as servers change.
	MCPToolCount func() int
	// Backups, when set, backs /undo and /backups. It may be nil.
	Backups *backup.Store
	// EstimateTokens, when set, approximates the token count of a message slice.
	// It backs /context. It may be nil.
	EstimateTokens func([]llm.Message) int
	// ContextLimit is the model context budget in tokens used by /context. Zero
	// means unknown.
	ContextLimit int
	// SwitchModel, when set, changes the model at runtime and returns the name
	// actually applied. It backs /model <name>.
	SwitchModel func(name string) (string, error)
	// MCPStatus, when set, returns a short multi-line report for /mcp.
	MCPStatus func() string
	// OnEvent, when set, receives every agent event in addition to the UI. It is
	// used for JSONL tracing.
	OnEvent func(zcode.Event)
}

// Run starts the interactive UI and blocks until the user quits.
func Run(ag *zcode.Agent, opts Options) error {
	m := newModel(ag, opts)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	m.program = p

	ag.OnDelta = func(s string) { p.Send(deltaMsg(s)) }
	extra := opts.OnEvent
	ag.OnEvent = func(e zcode.Event) {
		if extra != nil {
			extra(e)
		}
		p.Send(eventMsg(e))
	}

	// Permission prompts run on the agent goroutine; they hand the request to
	// the UI loop and block until the user answers. A handler set by the caller
	// (for example -y) takes precedence.
	if ag.OnPermission == nil {
		ag.OnPermission = func(req zcode.PermissionRequest) bool {
			reply := make(chan bool, 1)
			m.permReq = &req
			m.permReply = reply
			p.Send(permMsg{})
			return <-reply
		}
	}

	_, err := p.Run()
	return err
}

type role int

const (
	roleSystem role = iota
	roleUser
	roleAssistant
	roleTool
	roleError
)

type block struct {
	role role
	text string
	err  bool
}

// Messages exchanged between the running agent goroutine and the UI loop.
type (
	deltaMsg   string
	eventMsg   zcode.Event
	permMsg    struct{}
	compactMsg struct {
		changed bool
		err     error
	}
	doneMsg struct {
		answer string
		err    error
	}
)

type model struct {
	ag     *zcode.Agent
	opts   Options
	styles styles

	program *tea.Program

	viewport viewport.Model
	input    textarea.Model
	spinner  spinner.Model

	blocks []block
	stream strings.Builder

	busy     bool
	cancel   context.CancelFunc
	lastTool string
	status   string
	// pasteCollapsed hides a multi-line paste behind a one-line summary in the
	// input box while the full text stays in the textarea; an explicit non-rune
	// key (navigation, edit, submit) reveals the real content again.
	pasteCollapsed bool
	// The Windows console reader delivers a paste as a burst of single-rune
	// messages and leaves KeyMsg.Paste false, so key timing is the only signal
	// that a line break belongs to a paste rather than the user pressing Enter.
	lastInputAt    time.Time
	lastInputGap   time.Duration
	lastKeyWasRune bool
	prevKeyWasRune bool
	turns          int
	usage          llm.Usage

	// turnStart and lastCost measure how long the most recent request took; the
	// duration is shown in the status line once it finishes.
	turnStart time.Time
	lastCost  time.Duration

	// Pending tool-approval prompt. permReq is set by the agent goroutine in
	// Run's OnPermission before it sends permMsg, so the channel happens-before
	// edge makes reading it from the UI loop safe without a mutex.
	permReq   *zcode.PermissionRequest
	permReply chan bool

	width, height int
	ready         bool
}

func newModel(ag *zcode.Agent, opts Options) *model {
	ta := textarea.New()
	ta.Placeholder = "输入设计需求；Enter 发送，Ctrl+J 换行（可多行）"
	// No prompt glyph: input starts flush left. The active mode is shown in the
	// status bar instead of the input box.
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.MaxHeight = inputMinHeight
	ta.SetHeight(inputMinHeight)
	ta.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colAccent)

	return &model{
		ag:       ag,
		opts:     opts,
		styles:   newStyles(),
		viewport: viewport.New(0, 0),
		input:    ta,
		spinner:  sp,
	}
}

func (m *model) Init() tea.Cmd { return nil }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		now := time.Now()
		m.lastInputGap = now.Sub(m.lastInputAt)
		m.lastInputAt = now
		m.prevKeyWasRune = m.lastKeyWasRune
		m.lastKeyWasRune = k.Type == tea.KeyRunes
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.layout()
		return m, nil
	case tea.KeyMsg:
		if cmd, handled := m.handleKey(msg); handled {
			return m, cmd
		}
	case tea.MouseMsg:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case deltaMsg:
		m.stream.WriteString(string(msg))
		m.refresh()
		return m, nil
	case eventMsg:
		m.handleEvent(zcode.Event(msg))
		return m, nil
	case permMsg:
		m.layout()
		return m, nil
	case compactMsg:
		m.finishCompact(msg)
		return m, nil
	case doneMsg:
		m.finish(msg)
		return m, nil
	}

	// A rune insert can still be the tail of a paste, so keep the summary
	// hidden until an explicit non-rune key (a move, edit, or submit) arrives.
	if k, ok := msg.(tea.KeyMsg); ok && k.Type != tea.KeyRunes {
		m.pasteCollapsed = false
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.layout()
	return m, cmd
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	// A pending approval prompt captures every key. Ctrl+C denies and quits.
	if m.permReq != nil {
		if msg.Type == tea.KeyCtrlC {
			m.answerPermission(msg)
			return tea.Quit, true
		}
		return m.answerPermission(msg), true
	}
	// A pasted block is hidden behind a one-line summary while the full text is
	// inserted, so Enter still sends every line. Bracketed paste (Unix, or a
	// terminal that speaks it) sets msg.Paste; the Windows console reader
	// instead delivers the block as single-rune messages whose line break
	// arrives as its own rune.
	if msg.Paste {
		if text, multi := pasteText(msg.Runes); multi {
			m.collapsePaste(text)
			return nil, true
		}
	}
	if msg.Type == tea.KeyRunes {
		if text, multi := pasteText(msg.Runes); multi {
			m.collapsePaste(text)
			return nil, true
		}
	}
	switch msg.Type {
	case tea.KeyCtrlC:
		return tea.Quit, true
	case tea.KeyEsc:
		if m.busy && m.cancel != nil {
			m.cancel()
			m.status = "正在取消…"
			m.layout()
		}
		return nil, true
	case tea.KeyCtrlJ:
		m.input.InsertString("\n")
		m.pasteCollapsed = false
		m.layout()
		return nil, true
	case tea.KeyPgUp, tea.KeyPgDown:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return cmd, true
	case tea.KeyShiftTab:
		m.toggleMode()
		return nil, true
	case tea.KeyEnter:
		// On some Windows terminals a pasted line break arrives as Enter hot on
		// the heels of the characters before it; fold it back into the input
		// instead of submitting half the paste. A real Enter follows a pause,
		// so it is left to submit as usual.
		if m.lastInputGap <= pasteBurst && (m.prevKeyWasRune || m.pasteCollapsed) {
			m.input.InsertString("\n")
			m.pasteCollapsed = true
			m.layout()
			return nil, true
		}
		if m.busy {
			return nil, true
		}
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return nil, true
		}
		m.input.Reset()
		m.pasteCollapsed = false
		if strings.HasPrefix(text, "/") {
			return m.command(text), true
		}
		m.start(text)
		return m.run(text), true
	}
	return nil, false
}

func (m *model) start(text string) {
	m.flushStream()
	m.blocks = append(m.blocks, block{role: roleUser, text: text})
	m.stream.Reset()
	m.busy = true
	m.turns++
	m.turnStart = time.Now()
	m.lastTool = ""
	m.status = ""
	m.layout()
}

func (m *model) run(text string) tea.Cmd {
	ag := m.ag
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	work := func() tea.Msg {
		answer, err := ag.Ask(ctx, text)
		return doneMsg{answer: answer, err: err}
	}
	return tea.Batch(work, m.spinner.Tick)
}

func (m *model) finish(d doneMsg) {
	m.busy = false
	m.cancel = nil

	if d.err != nil {
		m.flushStream()
		m.blocks = append(m.blocks, block{role: roleError, text: "请求失败：" + d.err.Error()})
		m.status = "出错，可重新输入"
	} else {
		if m.stream.Len() == 0 && strings.TrimSpace(d.answer) != "" {
			m.stream.WriteString(d.answer)
		}
		m.flushStream()
		m.status = ""
	}
	m.usage = m.ag.Usage
	if !m.turnStart.IsZero() {
		m.lastCost = time.Since(m.turnStart)
		m.turnStart = time.Time{}
	}
	m.layout()
}

// finishCompact reports the outcome of a manual /compact run.
func (m *model) finishCompact(msg compactMsg) {
	m.busy = false
	switch {
	case msg.err != nil:
		m.blocks = append(m.blocks, block{role: roleError, text: "压缩失败：" + msg.err.Error()})
	case msg.changed:
		m.blocks = append(m.blocks, block{role: roleSystem, text: "已压缩上下文。"})
	default:
		m.blocks = append(m.blocks, block{role: roleSystem, text: "上下文未超过阈值，无需压缩。"})
	}
	m.status = ""
	m.layout()
}

func (m *model) handleEvent(e zcode.Event) {
	switch e.Kind {
	case zcode.EventToolCall:
		// Emit any assistant preamble before the tool lines.
		m.flushStream()
		m.lastTool = e.Tool
		m.blocks = append(m.blocks, block{role: roleTool, text: "→ " + e.Tool + " " + oneLine(e.Args, 160)})
	case zcode.EventToolResult:
		status := "ok"
		if e.Err != "" {
			status = "error: " + oneLine(e.Err, 120)
		}
		m.blocks = append(m.blocks, block{
			role: roleTool,
			err:  e.Err != "",
			text: fmt.Sprintf("← %s (%s, %d 字符)", e.Tool, status, len(e.Result)),
		})
		m.lastTool = ""
	case zcode.EventThought, zcode.EventAssistant:
		// With a non-streaming client the reply only arrives here; with a
		// streaming client the text is already buffered. Flushing is deferred so
		// the final answer is emitted exactly once (at done).
		if m.stream.Len() == 0 && e.Content != "" {
			m.stream.WriteString(e.Content)
		}
		m.usage = m.ag.Usage
	case zcode.EventPermission:
		m.flushStream()
		role := roleSystem
		if e.Result == "deny" {
			role = roleError
		}
		m.blocks = append(m.blocks, block{role: role, text: fmt.Sprintf("权限 %s：%s", e.Tool, e.Result)})
	case zcode.EventCompact:
		m.flushStream()
		m.blocks = append(m.blocks, block{role: roleSystem, text: "已压缩上下文：" + e.Result})
	case zcode.EventWarning:
		m.blocks = append(m.blocks, block{role: roleError, text: "警告：" + e.Err})
	case zcode.EventLimit:
		m.flushStream()
		m.blocks = append(m.blocks, block{role: roleError, text: e.Err})
	}
	m.layout()
}

func (m *model) flushStream() {
	s := strings.TrimRight(m.stream.String(), "\n")
	m.stream.Reset()
	if strings.TrimSpace(s) != "" {
		m.blocks = append(m.blocks, block{role: roleAssistant, text: s})
	}
}

func (m *model) command(line string) tea.Cmd {
	fields := strings.Fields(line)
	switch fields[0] {
	case "/exit", "/quit":
		return tea.Quit
	case "/reset":
		m.ag.Reset()
		m.blocks = nil
		m.stream.Reset()
		m.status = "已清空上下文"
	case "/clear":
		m.blocks = nil
		m.status = ""
	case "/model":
		if len(fields) > 1 {
			m.switchModel(strings.Join(fields[1:], " "))
		} else {
			m.blocks = append(m.blocks, block{role: roleSystem, text: "模型：" + m.opts.Model + "（/model <名字> 切换）"})
		}
	case "/cost":
		u := m.ag.Usage
		m.blocks = append(m.blocks, block{role: roleSystem, text: fmt.Sprintf("token 用量：prompt %d · completion %d · total %d", u.PromptTokens, u.CompletionTokens, u.TotalTokens)})
	case "/context":
		m.contextCommand()
	case "/compact":
		return m.compactCommand()
	case "/undo":
		m.undoCommand(fields)
	case "/backups":
		m.listBackups()
	case "/plan":
		m.ag.SetMode(zcode.ModePlan)
	case "/build":
		m.ag.SetMode(zcode.ModeBuild)
	case "/mode":
		m.blocks = append(m.blocks, block{role: roleSystem, text: "当前模式：" + string(m.ag.Mode)})
	case "/memory":
		m.memoryCommand(fields)
	case "/rules":
		body := strings.TrimSpace(m.opts.Rules)
		if body == "" {
			body = "（未加载项目规则；在 config.json 里配置 rules.files）"
		}
		m.blocks = append(m.blocks, block{role: roleSystem, text: "项目规则：\n" + body})
	case "/session":
		m.sessionCommand()
	case "/new":
		m.newSession()
	case "/sessions":
		m.listSessions()
	case "/resume":
		m.resumeSession(fields)
	case "/delete":
		m.deleteSession(fields)
	case "/rename":
		m.renameSession(fields)
	case "/mcp":
		m.mcpCommand()
	case "/skills":
		m.skillsCommand()
	case "/help":
		m.blocks = append(m.blocks, block{role: roleSystem, text: m.helpBody()})
	default:
		if custom, ok := m.opts.Commands[strings.TrimPrefix(fields[0], "/")]; ok {
			if m.busy {
				return nil
			}
			args := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
			return m.runCustom(custom, args)
		}
		m.blocks = append(m.blocks, block{role: roleError, text: "未知命令：" + fields[0] + "（输入 /help 查看）"})
	}
	m.layout()
	return nil
}

// contextCommand reports the estimated context size and its budget.
func (m *model) contextCommand() {
	msgs := m.ag.History()
	var b strings.Builder
	if m.opts.EstimateTokens != nil {
		n := m.opts.EstimateTokens(msgs)
		fmt.Fprintf(&b, "上下文估算：约 %d tokens", n)
		if m.opts.ContextLimit > 0 {
			fmt.Fprintf(&b, " / %d（%.0f%%）", m.opts.ContextLimit, 100*float64(n)/float64(m.opts.ContextLimit))
		}
	} else {
		b.WriteString("未启用上下文估算（在 config.json 里设置 context.max_tokens）")
	}
	fmt.Fprintf(&b, "\n消息数：%d", len(msgs))
	m.blocks = append(m.blocks, block{role: roleSystem, text: b.String()})
}

// compactCommand runs the compactor once against the current history. It is a
// no-op with a notice when compaction is not configured.
func (m *model) compactCommand() tea.Cmd {
	if m.busy {
		return nil
	}
	if m.ag.Compactor == nil {
		m.blocks = append(m.blocks, block{role: roleSystem, text: "未启用上下文压缩（在 config.json 里设置 context.max_tokens）"})
		m.layout()
		return nil
	}
	m.busy = true
	m.status = "正在压缩上下文…"
	m.layout()
	ag := m.ag
	work := func() tea.Msg {
		changed, err := ag.Compact(context.Background())
		return compactMsg{changed: changed, err: err}
	}
	return tea.Batch(work, m.spinner.Tick)
}

// undoCommand restores a pre-write snapshot and drops it so repeated /undo
// walks further back. An optional 1-based index (from /backups) picks a
// specific snapshot.
func (m *model) undoCommand(fields []string) {
	if m.opts.Backups == nil {
		m.blocks = append(m.blocks, block{role: roleSystem, text: "未启用备份（在 config.json 里设置 backup.enabled）"})
		return
	}
	entry, ok, err := m.pickBackup(fields)
	if err != nil {
		m.blocks = append(m.blocks, block{role: roleError, text: "撤销失败：" + err.Error()})
		return
	}
	if !ok {
		m.blocks = append(m.blocks, block{role: roleSystem, text: "没有可撤销的备份。"})
		return
	}
	if _, err := m.opts.Backups.Restore(entry.ID); err != nil {
		m.blocks = append(m.blocks, block{role: roleError, text: "撤销失败：" + err.Error()})
		return
	}
	_ = m.opts.Backups.Drop(entry.ID)
	m.blocks = append(m.blocks, block{role: roleSystem, text: "已撤销：" + entry.Source + "（可继续 /undo 回退更早的改动）"})
}

// pickBackup returns the snapshot to undo: the newest, or the entry at the
// 1-based index shown by /backups.
func (m *model) pickBackup(fields []string) (backup.Entry, bool, error) {
	if len(fields) < 2 {
		return m.opts.Backups.Latest()
	}
	n, err := strconv.Atoi(fields[1])
	if err != nil {
		return backup.Entry{}, false, fmt.Errorf("用法：/undo [序号]")
	}
	list, err := m.opts.Backups.List()
	if err != nil {
		return backup.Entry{}, false, err
	}
	if n < 1 || n > len(list) {
		return backup.Entry{}, false, fmt.Errorf("序号 %d 超出范围（共 %d 条）", n, len(list))
	}
	return list[n-1], true, nil
}

// listBackups shows recent snapshots, newest first.
func (m *model) listBackups() {
	if m.opts.Backups == nil {
		m.blocks = append(m.blocks, block{role: roleSystem, text: "未启用备份（在 config.json 里设置 backup.enabled）"})
		return
	}
	list, err := m.opts.Backups.List()
	if err != nil {
		m.blocks = append(m.blocks, block{role: roleError, text: "读取备份失败：" + err.Error()})
		return
	}
	if len(list) == 0 {
		m.blocks = append(m.blocks, block{role: roleSystem, text: "暂无备份。"})
		return
	}
	var b strings.Builder
	b.WriteString("备份（新→旧）")
	for i, e := range list {
		if i >= 10 {
			fmt.Fprintf(&b, "\n… 还有 %d 条", len(list)-i)
			break
		}
		fmt.Fprintf(&b, "\n  %d. %s  %s", i+1, e.ModTime.Format("01-02 15:04:05"), e.Source)
	}
	b.WriteString("\n/undo 恢复最近一次 · /undo <序号> 恢复指定备份")
	m.blocks = append(m.blocks, block{role: roleSystem, text: b.String()})
}

// switchModel changes the model used for subsequent requests.
func (m *model) switchModel(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		m.blocks = append(m.blocks, block{role: roleSystem, text: "用法：/model <模型名>"})
		return
	}
	if m.opts.SwitchModel == nil {
		m.blocks = append(m.blocks, block{role: roleSystem, text: "当前模型不支持运行时切换"})
		return
	}
	got, err := m.opts.SwitchModel(name)
	if err != nil {
		m.blocks = append(m.blocks, block{role: roleError, text: "切换模型失败：" + err.Error()})
		return
	}
	m.opts.Model = got
	m.blocks = append(m.blocks, block{role: roleSystem, text: "已切换模型：" + got})
}

// mcpCommand reports configured MCP servers and their tool counts.
func (m *model) mcpCommand() {
	body := "未启用 MCP（在 config.json 里设置 mcp.enabled 与 servers）"
	if m.opts.MCPStatus != nil {
		if s := strings.TrimSpace(m.opts.MCPStatus()); s != "" {
			body = s
		}
	}
	m.blocks = append(m.blocks, block{role: roleSystem, text: body})
}

// helpBody returns the built-in help plus any user-defined commands.
func (m *model) helpBody() string {
	if len(m.opts.Commands) == 0 {
		return helpText
	}
	names := make([]string, 0, len(m.opts.Commands))
	for name := range m.opts.Commands {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString(helpText)
	b.WriteString("\n\n自定义命令")
	for _, name := range names {
		b.WriteString("\n  /" + name)
		if desc := m.opts.Commands[name].Description; desc != "" {
			b.WriteString("  " + desc)
		}
	}
	return b.String()
}

// skillsCommand lists the skills the model can load on demand.
func (m *model) skillsCommand() {
	if len(m.opts.Skills) == 0 {
		m.blocks = append(m.blocks, block{role: roleSystem, text: "没有可用技能（在 .zcode/skills 放置 *.md）"})
		return
	}
	var b strings.Builder
	b.WriteString("可用技能")
	for _, s := range m.opts.Skills {
		b.WriteString("\n  " + s.Name)
		if s.Description != "" {
			b.WriteString("  " + s.Description)
		}
	}
	b.WriteString("\n模型会在需要时通过 skill 工具加载。")
	m.blocks = append(m.blocks, block{role: roleSystem, text: b.String()})
}

// runCustom expands a user-defined command and sends the result as a prompt.
func (m *model) runCustom(c command.Command, args string) tea.Cmd {
	display := "/" + c.Name
	if args != "" {
		display += " " + args
	}
	m.start(display)
	return m.run(c.Expand(args))
}

// answerPermission resolves a pending approval prompt. Only y/Y/Enter approve;
// any other key denies, so a stray keypress is always safe.
func (m *model) answerPermission(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	approve := key == "y" || key == "Y" || key == "enter"
	m.permReq = nil
	if m.permReply != nil {
		select {
		case m.permReply <- approve:
		default:
		}
		m.permReply = nil
	}
	action := "已拒绝"
	if approve {
		action = "已允许"
	}
	m.blocks = append(m.blocks, block{role: roleSystem, text: action})
	m.layout()
	return nil
}

// sessionCommand reports the active session.
func (m *model) sessionCommand() {
	if m.opts.Sessions == nil {
		m.blocks = append(m.blocks, block{role: roleSystem, text: "会话未启用（在 config.json 里设置 session.enabled）"})
		return
	}
	id, path, n := m.opts.Sessions.Current()
	if id == "" {
		m.blocks = append(m.blocks, block{role: roleSystem, text: "当前没有活动会话"})
		return
	}
	m.blocks = append(m.blocks, block{role: roleSystem, text: fmt.Sprintf("当前会话：%s（%d 条消息）\n%s", id, n, path)})
}

// newSession rotates to a fresh session and clears the conversation.
func (m *model) newSession() {
	if m.opts.Sessions == nil {
		m.blocks = append(m.blocks, block{role: roleSystem, text: "会话未启用（在 config.json 里设置 session.enabled）"})
		return
	}
	id, err := m.opts.Sessions.New()
	if err != nil {
		m.blocks = append(m.blocks, block{role: roleError, text: "新建会话失败：" + err.Error()})
		return
	}
	m.ag.Reset()
	m.blocks = nil
	m.stream.Reset()
	m.status = "新会话：" + id
}

// listSessions shows recent sessions, newest first, numbered for /resume,
// /delete, and /rename.
func (m *model) listSessions() {
	if m.opts.Sessions == nil {
		m.blocks = append(m.blocks, block{role: roleSystem, text: "会话未启用（在 config.json 里设置 session.enabled）"})
		return
	}
	infos, err := m.opts.Sessions.List()
	if err != nil {
		m.blocks = append(m.blocks, block{role: roleError, text: "读取会话失败：" + err.Error()})
		return
	}
	if len(infos) == 0 {
		m.blocks = append(m.blocks, block{role: roleSystem, text: "没有历史会话"})
		return
	}
	var b strings.Builder
	b.WriteString("历史会话（最新在前）")
	cur, _, _ := m.opts.Sessions.Current()
	for i, info := range infos {
		if i >= 10 {
			b.WriteString(fmt.Sprintf("\n… 还有 %d 条", len(infos)-i))
			break
		}
		mark := "  "
		if info.ID == cur {
			mark = "* "
		}
		b.WriteString(fmt.Sprintf("\n%s%d. %s  %d 条  %s",
			mark, i+1, info.ID, info.Messages, info.ModTime.Format("01-02 15:04")))
		if info.Preview != "" {
			b.WriteString("  " + info.Preview)
		}
	}
	b.WriteString("\n/resume <序号|id> · /delete <序号|id> · /rename <序号|id> <新名>")
	m.blocks = append(m.blocks, block{role: roleSystem, text: b.String()})
}

// sessionByRef resolves a 1-based list index (as shown by /sessions) or a raw
// session id to an id.
func (m *model) sessionByRef(ref string) (string, error) {
	if n, err := strconv.Atoi(ref); err == nil {
		infos, err := m.opts.Sessions.List()
		if err != nil {
			return "", err
		}
		if n < 1 || n > len(infos) {
			return "", fmt.Errorf("序号 %d 超出范围（共 %d 条）", n, len(infos))
		}
		return infos[n-1].ID, nil
	}
	return ref, nil
}

// deleteSession removes a stored session.
func (m *model) deleteSession(fields []string) {
	if m.opts.Sessions == nil {
		m.blocks = append(m.blocks, block{role: roleSystem, text: "会话未启用（在 config.json 里设置 session.enabled）"})
		return
	}
	if len(fields) < 2 {
		m.blocks = append(m.blocks, block{role: roleSystem, text: "用法：/delete <序号|id>（用 /sessions 查看）"})
		return
	}
	id, err := m.sessionByRef(fields[1])
	if err != nil {
		m.blocks = append(m.blocks, block{role: roleError, text: "删除失败：" + err.Error()})
		return
	}
	if err := m.opts.Sessions.Delete(id); err != nil {
		m.blocks = append(m.blocks, block{role: roleError, text: "删除会话失败：" + err.Error()})
		return
	}
	m.blocks = append(m.blocks, block{role: roleSystem, text: "已删除会话：" + id})
}

// renameSession gives a stored session a new id.
func (m *model) renameSession(fields []string) {
	if m.opts.Sessions == nil {
		m.blocks = append(m.blocks, block{role: roleSystem, text: "会话未启用（在 config.json 里设置 session.enabled）"})
		return
	}
	if len(fields) < 3 {
		m.blocks = append(m.blocks, block{role: roleSystem, text: "用法：/rename <序号|id> <新名>"})
		return
	}
	id, err := m.sessionByRef(fields[1])
	if err != nil {
		m.blocks = append(m.blocks, block{role: roleError, text: "重命名失败：" + err.Error()})
		return
	}
	newID := fields[2]
	if err := m.opts.Sessions.Rename(id, newID); err != nil {
		m.blocks = append(m.blocks, block{role: roleError, text: "重命名会话失败：" + err.Error()})
		return
	}
	m.blocks = append(m.blocks, block{role: roleSystem, text: fmt.Sprintf("已重命名会话：%s → %s", id, newID)})
}

// resumeSession loads a stored session and replays it into the view.
func (m *model) resumeSession(fields []string) {
	if m.opts.Sessions == nil {
		m.blocks = append(m.blocks, block{role: roleSystem, text: "会话未启用（在 config.json 里设置 session.enabled）"})
		return
	}
	if len(fields) < 2 {
		m.blocks = append(m.blocks, block{role: roleSystem, text: "用法：/resume <序号|id>（用 /sessions 查看）"})
		return
	}
	ref, err := m.sessionByRef(fields[1])
	if err != nil {
		m.blocks = append(m.blocks, block{role: roleError, text: "恢复会话失败：" + err.Error()})
		return
	}
	id, err := m.opts.Sessions.Open(ref)
	if err != nil {
		m.blocks = append(m.blocks, block{role: roleError, text: "恢复会话失败：" + err.Error()})
		return
	}
	if err := m.ag.Restore(); err != nil {
		m.blocks = append(m.blocks, block{role: roleError, text: "加载会话失败：" + err.Error()})
		return
	}
	m.blocks = nil
	m.stream.Reset()
	m.replayTranscript()
	m.status = "已恢复会话：" + id
}

// replayTranscript renders the restored transcript so a resumed session shows
// its history.
func (m *model) replayTranscript() {
	if m.opts.Sessions == nil {
		return
	}
	msgs, err := m.opts.Sessions.Load()
	if err != nil {
		return
	}
	for _, msg := range msgs {
		switch msg.Role {
		case "user":
			if strings.TrimSpace(msg.Content) != "" {
				m.blocks = append(m.blocks, block{role: roleUser, text: msg.Content})
			}
		case "assistant":
			if strings.TrimSpace(msg.Content) != "" {
				m.blocks = append(m.blocks, block{role: roleAssistant, text: msg.Content})
			}
		}
	}
}

// toggleMode switches between plan and build, preserving the conversation. The
// active mode is shown by the status-bar badge, so no chat notice is added.
func (m *model) toggleMode() {
	if m.ag.Mode == zcode.ModePlan {
		m.ag.SetMode(zcode.ModeBuild)
	} else {
		m.ag.SetMode(zcode.ModePlan)
	}
}

// memoryCommand handles /memory and its subcommands.
func (m *model) memoryCommand(fields []string) {
	if m.opts.Store == nil {
		m.blocks = append(m.blocks, block{role: roleSystem, text: "记忆未启用（在 config.json 里设置 memory.enabled）"})
		return
	}
	switch {
	case len(fields) >= 2 && fields[1] == "clear":
		n := m.opts.Store.Clear()
		m.blocks = append(m.blocks, block{role: roleSystem, text: fmt.Sprintf("已清空 %d 条记忆", n)})
	case len(fields) >= 3 && fields[1] == "add":
		text := strings.TrimSpace(strings.Join(fields[2:], " "))
		_, added, err := m.opts.Store.Add(text, nil)
		switch {
		case err != nil:
			m.blocks = append(m.blocks, block{role: roleError, text: "记忆写入失败：" + err.Error()})
		case added:
			m.blocks = append(m.blocks, block{role: roleSystem, text: "已记住：" + text})
		default:
			m.blocks = append(m.blocks, block{role: roleSystem, text: "已存在相同记忆"})
		}
	default:
		body := m.opts.Store.Render()
		if strings.TrimSpace(body) == "" {
			body = "（暂无记忆）"
		}
		m.blocks = append(m.blocks, block{
			role: roleSystem,
			text: fmt.Sprintf("项目记忆（%d 条）：\n%s", m.opts.Store.Count(), body),
		})
	}
}

// layout recomputes widget sizes for the current window and input height.
func (m *model) layout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	// The input box keeps a fixed height; longer content scrolls inside it.
	lines := inputMinHeight
	m.input.SetHeight(lines)
	m.input.SetWidth(m.width - 4)

	// Chrome: input box = lines + 2 (border), plus the status line = lines + 3.
	vh := m.height - lines - 3
	if vh < 1 {
		vh = 1
	}
	m.viewport.Width = m.width
	m.viewport.Height = vh
	m.refresh()
}

func (m *model) refresh() {
	if m.width <= 0 {
		return
	}
	var sb strings.Builder
	for i := range m.blocks {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(m.renderBlock(m.blocks[i], m.width))
	}
	if m.stream.Len() > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(m.renderBlock(block{role: roleAssistant, text: m.stream.String()}, m.width))
	}
	if m.busy {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(m.spinner.View() + " " + m.styles.muted.Render(m.activity()))
	}

	atBottom := m.viewport.AtBottom()
	m.viewport.SetContent(sb.String())
	if atBottom || m.busy {
		m.viewport.GotoBottom()
	}
}

func (m *model) renderBlock(b block, width int) string {
	body := wrapText(b.text, width-2)
	switch b.role {
	case roleUser:
		return m.styles.userLabel.Render("● 你") + "\n" + body
	case roleAssistant:
		return m.styles.assistLabel.Render("◆ zcode") + "\n" + body
	case roleTool:
		if b.err {
			return m.styles.toolErr.Render(body)
		}
		return m.styles.tool.Render(body)
	case roleError:
		return m.styles.err.Render(body)
	default:
		return m.styles.system.Render(body)
	}
}

func (m *model) activity() string {
	if m.lastTool != "" {
		return "正在调用 " + m.lastTool + "…"
	}
	return "思考中…"
}

func (m *model) View() string {
	if !m.ready {
		return ""
	}
	// Pin the input box to the terminal width: lipgloss otherwise shrinks it to
	// the widest line, so a short collapsed summary or approval prompt would
	// make the box narrower than the rest of the UI. Width includes padding but
	// not the border, hence the -2.
	box := m.styles.inputBox
	if w := m.width - 2; w > 0 {
		box = box.Width(w)
	}
	var b strings.Builder
	b.WriteString(m.viewport.View())
	b.WriteString("\n")
	if m.permReq != nil {
		b.WriteString(box.Render(m.permissionView()))
	} else {
		b.WriteString(box.Render(m.inputDisplay()))
	}
	b.WriteString("\n")
	b.WriteString(m.statusView())
	return b.String()
}

// inputDisplay renders the input box body: a one-line summary for a collapsed
// multi-line paste, otherwise the full editor.
func (m *model) inputDisplay() string {
	if m.pasteCollapsed {
		width := m.width - 4
		if width < 10 {
			width = 10
		}
		// Pad to the fixed box height so collapsing never resizes the box.
		return padLines(collapsedPaste(m.input.Value(), width), inputMinHeight)
	}
	return m.input.View()
}

// padLines appends empty lines so s occupies at least n rows.
func padLines(s string, n int) string {
	if n < 2 {
		return s
	}
	return s + strings.Repeat("\n", n-1)
}

// pasteText normalizes pasted text and reports whether it spans multiple lines.
func pasteText(runes []rune) (string, bool) {
	if len(runes) == 0 {
		return "", false
	}
	s := strings.ReplaceAll(string(runes), "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s, strings.Contains(s, "\n")
}

// collapsePaste inserts a pasted block and switches the input box to its
// one-line summary; the full text stays in the textarea and is what gets sent.
func (m *model) collapsePaste(text string) {
	m.input.InsertString(text)
	m.pasteCollapsed = true
	m.layout()
}

// collapsedPaste summarizes multi-line text as "first .... last（共 N 行）",
// keeping the head and tail visible within width display columns. Blank and
// decorative lines (rules such as "======" or "------") are skipped when
// choosing the preview, so a separator cannot crowd out the real content.
func collapsedPaste(full string, width int) string {
	lines := strings.Split(strings.ReplaceAll(full, "\r\n", "\n"), "\n")
	if len(lines) < 2 {
		return truncateWidth(full, width)
	}
	suffix := fmt.Sprintf("（共 %d 行）", len(lines))
	first, okFirst := previewLine(lines, false)
	last, okLast := previewLine(lines, true)
	if !okFirst && !okLast {
		// Nothing but blanks and separators: just report the size.
		return truncateWidth(suffix, width)
	}
	if !okFirst {
		first = last
	}
	if !okLast {
		last = first
	}
	if first == last {
		return truncateWidth(first+suffix, width)
	}
	const sep = " .... "
	budget := width - lipgloss.Width(sep) - lipgloss.Width(suffix)
	if budget < 6 {
		// Not enough room for two ends; fall back to a plain head/tail cut.
		return truncateWidth(first+sep+last+suffix, width)
	}
	head := truncateWidth(first, budget/2)
	tail := truncateWidth(last, budget-budget/2)
	return head + sep + tail + suffix
}

// previewLine returns the first (fromEnd=false) or last (fromEnd=true) line
// that carries real content, skipping blanks and decorative separators. It
// reports false when every line is blank or decorative.
func previewLine(lines []string, fromEnd bool) (string, bool) {
	for i := range lines {
		idx := i
		if fromEnd {
			idx = len(lines) - 1 - i
		}
		if t := strings.TrimSpace(lines[idx]); t != "" && !isDecorative(t) {
			return t, true
		}
	}
	return "", false
}

// isDecorative reports whether a line is only a separator/rule (for example
// "=====", "-----", "|---|---|"), which makes a poor summary preview.
func isDecorative(s string) bool {
	const marks = "=+-_*#~<>|/\\:;,.·•─═━▬▭■□▪▫"
	if len([]rune(s)) < 3 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune(marks, r) {
			return false
		}
	}
	return true
}

// formatDuration renders a request duration compactly for the status line.
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// permissionView renders the pending tool-approval prompt, including the tool's
// own preview of what the call will do when it provides one.
func (m *model) permissionView() string {
	head := "允许 " + m.permReq.Tool + " 执行此操作？ [y] 允许 · [n] 拒绝"
	if p := strings.TrimSpace(m.permReq.Preview); p != "" {
		width := m.width - 4
		if width < 20 {
			width = 20
		}
		head += "\n" + truncateLines(p, 14, width)
	}
	return head
}

// statusView renders the bottom line: key hints (or the transient status), then
// the mode badge and workspace (model + working directory), then the token
// counters right-aligned. The hint is truncated first so the line never wraps.
func (m *model) statusView() string {
	right := fmt.Sprintf("tokens %d↑ %d↓  ", m.usage.PromptTokens, m.usage.CompletionTokens)
	if m.lastCost > 0 {
		right = formatDuration(m.lastCost) + "  " + right
	}
	rightW := lipgloss.Width(right)

	badge := m.styles.modePlan.Render("PLAN")
	if m.ag.Mode == zcode.ModeBuild {
		badge = m.styles.modeBuild.Render("BUILD")
	}
	info := badge + m.styles.muted.Render("  "+m.opts.Model)
	if m.opts.Root != "" {
		info += m.styles.muted.Render("  " + m.opts.Root)
	}
	if m.opts.Store != nil {
		info += m.styles.muted.Render(fmt.Sprintf("  mem %d", m.opts.Store.Count()))
	}
	if m.opts.MCPToolCount != nil {
		if n := m.opts.MCPToolCount(); n > 0 {
			info += m.styles.muted.Render(fmt.Sprintf("  mcp %d", n))
		}
	}
	// Keep a little room for the hint; drop the optional extras when the line
	// is tight.
	if lipgloss.Width(info) > m.width-rightW-24 {
		info = badge + m.styles.muted.Render("  "+m.opts.Model)
	}
	infoW := lipgloss.Width(info)

	hint := " Shift+Tab 模式 · /help"
	if m.permReq != nil {
		hint = " y 允许 · n 拒绝"
	} else if m.status != "" {
		hint = " " + m.status
	} else if m.busy {
		hint = " Esc 取消"
	}
	hint = truncateWidth(hint, m.width-rightW-infoW-2)
	if hint == "" {
		hint = " "
	}

	gap := m.width - lipgloss.Width(hint) - 1 - infoW - rightW
	if gap < 1 {
		gap = 1
	}
	return m.styles.status.Render(hint) + " " + info + strings.Repeat(" ", gap) + m.styles.status.Render(right)
}

// oneLine collapses whitespace and truncates to max runes.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); max > 0 && len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
