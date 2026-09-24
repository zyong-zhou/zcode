// Command zcode runs the program-design agent from the terminal.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhouyong/zcode/internal/backup"
	"github.com/zhouyong/zcode/internal/command"
	"github.com/zhouyong/zcode/internal/compact"
	"github.com/zhouyong/zcode/internal/config"
	"github.com/zhouyong/zcode/internal/llm"
	"github.com/zhouyong/zcode/internal/mcp"
	"github.com/zhouyong/zcode/internal/memory"
	"github.com/zhouyong/zcode/internal/observe"
	"github.com/zhouyong/zcode/internal/permission"
	"github.com/zhouyong/zcode/internal/rules"
	"github.com/zhouyong/zcode/internal/session"
	"github.com/zhouyong/zcode/internal/skill"
	"github.com/zhouyong/zcode/internal/tools"
	"github.com/zhouyong/zcode/internal/tui"
	"github.com/zhouyong/zcode/internal/zcode"
)

const buildPrompt = `你是 zcode 的 Build 模式，负责把用户的程序设计需求落地。
运行环境：Windows。命令不经过 shell，按「可执行文件名 + 参数数组」直接调用，不要用管道、重定向或 shell 内置命令。
工作方式：
1. 先用工具调研现有代码库（list_dir 看结构、search 搜代码、read_file 读文件），不要凭空假设。
2. 读写文件一律用内置工具：read_file 读、write_file 写、edit 定点改；不要用 cat、type、ls、dir、findstr、Get-Content 等命令代替。
3. run_command 只用于构建、测试与运行程序（如 go build ./...、go vet ./...、go test ./...），不要用它读写文件。
4. 需要时直接修改代码（write_file/edit）或执行命令（run_command）。
5. 改动要小、贴合现有风格；完成后运行相关检查并汇报改了什么、结果如何。
6. 与项目相关的长期事实（约定、决策、坑）用 remember 记录。
只输出与任务相关的内容，不要寒暄。`

const planPrompt = `你是 zcode 的 Plan 模式，负责调研并给出可执行的方案，不做任何改动。
运行环境：Windows。命令不经过 shell，按「可执行文件名 + 参数数组」直接调用。
工作方式：
1. 只用只读工具调研（list_dir、search、read_file、recall），不要假设。
2. 读写文件一律用内置工具：read_file 读、list_dir 列目录、search 检索；不要用 cat、type、ls、dir、findstr、Get-Content 等命令代替。
3. 不要修改文件、不要执行命令（这些工具在 Plan 模式不可用）。
4. 输出结构化方案，包含：需求理解、现状、改动点（精确到文件与函数）、步骤清单、风险、验证方式。
5. 项目相关的长期事实用 remember 记录。
只输出方案，不要实施改动。`

// version is the release version. It is overridden at build time with:
//
//	go build -ldflags "-X main.version=1.2.3" ./cmd/zcode
var version = "0.5.0"

// defaultRepeatLimit bounds identical tool calls per run.
const defaultRepeatLimit = 3

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	// Use a fresh FlagSet so run can be called more than once in tests without
	// duplicate flag registration.
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	var (
		request     = flag.String("request", "", "program design request (omit to launch the interactive UI in a terminal)")
		dir         = flag.String("dir", "", "directory tools operate in (default: current working directory)")
		out         = flag.String("out", "", "optional file to write the final design to")
		configPath  = flag.String("config", "", "path to the JSON config file (default: next to the executable, else ./config.json)")
		verbose     = flag.Bool("v", false, "print per-step progress to stderr (one-shot mode only)")
		tracePath   = flag.String("trace", "", "write a JSONL trace of each step to this file")
		interactive = flag.Bool("i", false, "launch the interactive terminal UI")
		forceUI     = flag.Bool("tui", false, "launch the interactive terminal UI (same as -i)")
		modeFlag    = flag.String("mode", "", "agent mode: plan (read-only) or build (default for interactive)")
		autoYes     = flag.Bool("y", false, "auto-approve tool permission prompts (ask decisions become allow)")
		sessionID   = flag.String("session", "", "resume a stored session by id")
		resumeLast  = flag.Bool("continue", false, "resume the most recent session")
		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("zcode", version)
		return nil
	}

	// The interactive UI is the default when no request is given in a terminal.
	useUI := *interactive || *forceUI
	if !useUI && *request == "" && stdinIsTerminal() {
		useUI = true
	}
	if *request == "" && !useUI {
		flag.Usage()
		return fmt.Errorf("-request is required (or run without it in a terminal for the interactive UI)")
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	root, err := toolRoot(*dir)
	if err != nil {
		return err
	}

	// Mode: config default, then -mode, then plan for one-shot runs.
	mode := zcode.ParseMode(cfg.Mode)
	if *modeFlag != "" {
		mode = zcode.ParseMode(*modeFlag)
	} else if *request != "" {
		mode = zcode.ModePlan
	}

	// Pre-write snapshots power the /undo command; the directory is gitignored.
	var backups *backup.Store
	if cfg.BackupOn() {
		backups = &backup.Store{Dir: resolvePath(root, cfg.Backup.Dir), Root: root}
	}
	var backupHook func(source, abs string) error
	if backups != nil {
		backupHook = func(source, abs string) error {
			_, err := backups.Snapshot(source, abs)
			return err
		}
	}

	reg := tools.NewRegistry(
		tools.ListDir{Root: root},
		tools.ReadFile{Root: root},
		tools.Search{Root: root},
		tools.WriteFile{Root: root, Backup: backupHook},
		tools.Edit{Root: root, Backup: backupHook},
		tools.RunCommand{Dir: root},
	)

	// Project memory: tools plus prompt injection.
	var store *memory.Store
	if cfg.MemoryOn() {
		st, err := memory.Open(resolvePath(root, cfg.Memory.Path))
		if err != nil {
			return err
		}
		store = st
		reg.Register(tools.Remember{Store: st})
		reg.Register(tools.Recall{Store: st})
		reg.Register(tools.Forget{Store: st})
	}

	// Project rules (AGENTS.md): injected into the system prompt.
	var rulesStore *rules.Store
	if cfg.RulesOn() {
		rs, err := rules.Load(root, cfg.Rules.Files, 0)
		if err != nil {
			return err
		}
		rulesStore = rs
	}

	// User-defined slash commands and on-demand skills, loaded from the project.
	commands, err := command.Load(resolvePath(root, ".zcode/commands"))
	if err != nil {
		return err
	}
	skills, err := skill.Load(resolvePath(root, ".zcode/skills"))
	if err != nil {
		return err
	}
	if len(skills) > 0 {
		infos := make([]tools.SkillInfo, 0, len(skills))
		for _, s := range skills {
			infos = append(infos, tools.SkillInfo{Name: s.Name, Description: s.Description, Body: s.Body})
		}
		reg.Register(tools.SkillTool{Skills: infos})
	}

	// Conversation sessions: persist the transcript so runs can be resumed.
	// -continue resumes the latest session; -session resumes a specific one;
	// otherwise every run starts a fresh session.
	resuming := *sessionID != "" || *resumeLast
	var sessions *session.Manager
	if cfg.SessionOn() {
		sessions = session.NewManager(session.Store{Dir: resolvePath(root, cfg.Session.Dir)})
		switch {
		case *resumeLast:
			id, err := sessions.Latest()
			if err != nil {
				return err
			}
			if id == "" {
				if _, err := sessions.New(); err != nil {
					return err
				}
			} else if _, err := sessions.Open(id); err != nil {
				return err
			}
		case *sessionID != "":
			if _, err := sessions.Open(*sessionID); err != nil {
				return err
			}
		default:
			if _, err := sessions.New(); err != nil {
				return err
			}
		}
		defer func() { _ = sessions.Close() }()
	} else if resuming {
		return fmt.Errorf("-session and -continue require sessions; set session.enabled to true")
	}

	// MCP servers: register their tools like any other. The manager reports
	// tool-list changes so the registry stays in step with the server.
	var mgr *mcp.Manager
	if cfg.MCPOn() {
		if cfgs := mcpConfigs(cfg); len(cfgs) > 0 {
			m, errs := mcp.Start(context.Background(), cfgs, mcp.ClientInfo{
				Name: "zcode", Version: version, Root: root,
			})
			mgr = m
			for _, e := range errs {
				fmt.Fprintln(os.Stderr, "note:", e)
			}
			var registered []string
			sync := func() {
				next := make(map[string]bool)
				for _, t := range mgr.Tools() {
					reg.Register(t)
					next[t.Name()] = true
				}
				for _, name := range registered {
					if !next[name] {
						reg.Unregister(name)
					}
				}
				registered = registered[:0]
				for _, t := range mgr.Tools() {
					registered = append(registered, t.Name())
				}
			}
			sync()
			mgr.SetOnChange(sync)
			defer func() { _ = mgr.Close() }()
		}
	}

	base := llm.NewOpenAIClient(cfg.BaseURL, cfg.APIKey, cfg.Model)
	var client llm.Client = llm.RetryClient{Inner: base, Attempts: 3, Base: 500 * time.Millisecond}

	// Context compaction: summarize history once it nears the window. It is off
	// unless context.max_tokens is set.
	var compactor zcode.Compactor
	if cfg.Context.MaxTokens > 0 {
		compactor = compact.New(compact.Options{
			MaxTokens:  cfg.Context.MaxTokens,
			TriggerAt:  cfg.Context.CompactAt,
			KeepRecent: cfg.Context.KeepRecent,
		}, newSummarizer(client))
	}
	// estimateTokens backs the TUI's /context command; it is only meaningful
	// when a context budget is configured.
	var estimateTokens func([]llm.Message) int
	if cfg.Context.MaxTokens > 0 {
		estimateTokens = compact.EstimateTokens
	}

	var mem zcode.MemoryProvider
	if store != nil {
		mem = store
	}
	var rulesProvider zcode.MemoryProvider
	if rulesStore != nil {
		rulesProvider = rulesStore
	}
	policy := permission.New(
		cfg.Permissions.Default,
		cfg.Permissions.Tools,
		cfg.Permissions.Commands.Allow,
		cfg.Permissions.Commands.Deny,
	)
	var onPermission zcode.OnPermission
	if *autoYes {
		onPermission = func(zcode.PermissionRequest) bool { return true }
	}
	var transcript zcode.Transcript
	if sessions != nil {
		transcript = sessions
	}
	a := zcode.New(client, reg, zcode.Options{
		System:       systemFor(cfg.Modes.Build.System, buildPrompt),
		PlanSystem:   systemFor(cfg.Modes.Plan.System, planPrompt),
		Mode:         mode,
		MaxSteps:     cfg.MaxSteps,
		Timeout:      time.Duration(cfg.TimeoutSeconds) * time.Second,
		RepeatLimit:  defaultRepeatLimit,
		Memory:       mem,
		Rules:        rulesProvider,
		Permissions:  func(tool, args string) string { return string(policy.Decide(tool, args)) },
		OnPermission: onPermission,
		Transcript:   transcript,
		Compactor:    compactor,
	})

	// Resuming loads the stored transcript into history.
	if resuming {
		if err := a.Restore(); err != nil {
			return err
		}
	}

	// In UI mode the UI owns the display, so -v is ignored (use -trace instead);
	// the sink still feeds the trace file.
	sink, closeTrace, err := eventSink(*verbose && !useUI, *tracePath)
	if err != nil {
		return err
	}
	defer closeTrace()

	if useUI {
		if !stdinIsTerminal() {
			return fmt.Errorf("the interactive UI needs a terminal; use -request for non-interactive runs")
		}
		if *out != "" {
			fmt.Fprintln(os.Stderr, "note: -out is ignored in interactive mode")
		}
		if *verbose {
			fmt.Fprintln(os.Stderr, "note: -v is ignored in the interactive UI (it has its own display); use -trace to record steps")
		}
		return tui.Run(a, tui.Options{
			Root:     root,
			Model:    cfg.Model,
			Store:    store,
			Rules:    rulesStore.Render(),
			Sessions: sessions,
			Commands: commands,
			Skills:   skills,
			MCPToolCount: func() int {
				if mgr == nil {
					return 0
				}
				return len(mgr.Tools())
			},
			EstimateTokens: estimateTokens,
			ContextLimit:   cfg.Context.MaxTokens,
			Backups:        backups,
			OnEvent:        sink,
		})
	}

	a.OnEvent = sink

	var design string
	if resuming {
		design, err = a.Ask(context.Background(), *request)
	} else {
		design, err = a.Design(context.Background(), *request)
	}
	if err != nil {
		return err
	}
	if *verbose {
		printUsage(a.Usage)
	}
	if *out != "" {
		if err := os.WriteFile(*out, []byte(design), 0o644); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "wrote", *out)
		return nil
	}
	fmt.Println(design)
	return nil
}

// newSummarizer returns a compact.Summarizer backed by the configured model.
func newSummarizer(client llm.Client) compact.Summarizer {
	return func(ctx context.Context, text string) (string, error) {
		resp, err := client.Chat(ctx, llm.Request{Messages: []llm.Message{
			{Role: "system", Content: "你是对话压缩器。请用中文简洁总结下面的对话，保留目标、已确认的事实、文件路径、待办事项与关键决策。只输出摘要正文。"},
			{Role: "user", Content: text},
		}})
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(resp.Content), nil
	}
}

// systemFor returns override when set, otherwise the built-in prompt.
func systemFor(override, builtin string) string {
	if override != "" {
		return override
	}
	return builtin
}

// toolRoot resolves the directory tools operate in. An explicit -dir wins.
// Otherwise the executable's directory is used, so a double-clicked
// bin\zcode.exe operates on its own folder. Under `go run` the executable lives
// in a temporary build directory, so the current working directory is used
// instead.
func toolRoot(dir string) (string, error) {
	if dir != "" {
		return filepath.Abs(dir)
	}
	if exe, err := os.Executable(); err == nil {
		if d := filepath.Dir(exe); !isGoRunDir(d) {
			return d, nil
		}
	}
	return filepath.Abs("")
}

// isGoRunDir reports whether dir looks like a `go run` temporary build
// directory (…/go-build…/bNNN/exe).
func isGoRunDir(dir string) bool {
	return strings.Contains(strings.ToLower(dir), "go-build")
}

// resolvePath joins a config-relative path against root, leaving absolute paths
// untouched.
func resolvePath(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

// mcpConfigs converts enabled config servers into mcp.ServerConfig values.
func mcpConfigs(cfg config.Config) []mcp.ServerConfig {
	var out []mcp.ServerConfig
	for _, s := range cfg.MCP.Servers {
		if !s.On() || s.Command == "" {
			continue
		}
		out = append(out, mcp.ServerConfig{
			Name:     s.Name,
			Command:  s.Command,
			Args:     s.Args,
			Env:      s.Env,
			ReadOnly: s.ReadOnly,
		})
	}
	return out
}

func printUsage(u llm.Usage) {
	fmt.Fprintf(os.Stderr, "[done] tokens(p/c/t)=%d/%d/%d\n", u.PromptTokens, u.CompletionTokens, u.TotalTokens)
}

// stdinIsTerminal reports whether stdin is attached to a terminal, so we only
// auto-enter interactive mode for humans, not for piped input.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// loadConfig merges configuration sources. Precedence, lowest to highest:
// built-in defaults, the JSON file, LLM_*/ZCODE_* environment variables. When
// -config is empty the file is looked up next to the executable and then in the
// current directory. A missing file is ignored unless the user passed -config
// explicitly, which is treated as an error so typos do not silently fall back
// to defaults.
func loadConfig(path string) (config.Config, error) {
	explicit := path != ""
	if !explicit {
		path = defaultConfigPath()
	}

	cfg := config.Default()
	if _, statErr := os.Stat(path); statErr == nil {
		loaded, err := config.Load(path)
		if err != nil {
			return cfg, err
		}
		cfg = loaded
	} else if explicit {
		return cfg, fmt.Errorf("config: %s: %w", path, statErr)
	}

	cfg.ApplyEnv()
	cfg.Normalize()
	return cfg, nil
}

// eventSink fans agent events out to the configured observers and returns a
// close function for the trace file, if one was opened.
func eventSink(verbose bool, tracePath string) (func(zcode.Event), func(), error) {
	var observers []func(zcode.Event)
	if verbose {
		observers = append(observers, observe.Text(os.Stderr))
	}
	closeFn := func() {}
	if tracePath != "" {
		f, err := os.Create(tracePath)
		if err != nil {
			return nil, closeFn, fmt.Errorf("trace: %w", err)
		}
		closeFn = func() { _ = f.Close() }
		observers = append(observers, observe.NewTrace(f).Observe)
	}
	return func(e zcode.Event) {
		for _, obs := range observers {
			obs(e)
		}
	}, closeFn, nil
}

// defaultConfigPath prefers config.json next to the executable so a packaged
// folder is self-contained, and falls back to ./config.json (the dev layout,
// where the binary lives in a temporary build directory).
func defaultConfigPath() string {
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "config.json")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "config.json"
}
