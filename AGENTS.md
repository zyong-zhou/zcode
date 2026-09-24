# AGENTS.md

zcode 是 Go 实现的「程序设计 Agent」：CLI 驱动一个 LLM，让它通过工具（读写文件、执行命令）调研代码库并产出结构化设计文档。本文件只记录 Agent 容易猜错、且从文件名看不出的事实。

## 结构
- `cmd/zcode/`：唯一入口。只负责解析 flag、加载配置、装配依赖，不含业务逻辑。
- `internal/config/`：配置加载。JSON 文件 + 环境变量覆盖，优先级在 `cmd/zcode.loadConfig` 合并。
- `internal/llm/`：模型后端。`Client` 接口是唯一边界；`OpenAIClient` 兼容任何 OpenAI 风格的 `/chat/completions`。
- `internal/zcode/`：Agent 主循环（推理 → 工具调用 → 观察 → 再推理），包名 `zcode`、主类型 `zcode.Agent`，通过 `OnEvent` 发出可观测事件。
- `internal/observe/`：事件消费者。`Text` 输出逐步进度，`Trace` 写 JSONL，供 `cmd/zcode` 的 `-v`/`-trace` 使用。
- `internal/tools/`：Agent 可调用的工具及 `Registry`。工具可声明 `PlanSafe() bool`（见下）。
- `internal/memory/`：项目记忆存储，落盘在 `.zcode/memory.md`（唯一真源，可手改）。
- `internal/rules/`：项目规则（默认 `AGENTS.md`）加载，注入系统提示词；缺失文件跳过。
- `internal/session/`：会话持久化。JSONL 逐条消息落盘；`Manager` 实现 `zcode.Transcript` 并保证换会话时线程安全。
- `internal/compact/`：上下文压缩（先裁剪旧工具结果，再让模型摘要早期对话），实现 `zcode.Compactor`。
- `internal/permission/`：工具权限策略（allow/ask/deny，含 `run_command` 命令前缀白/黑名单）。
- `internal/command/`：自定义斜杠命令（`.zcode/commands/*.md`，支持 `$ARGUMENTS`/`$1`）。
- `internal/skill/`：按需加载的技能（`.zcode/skills/*.md`），经 `tools.SkillTool` 暴露。
- `internal/mcp/`：MCP stdio 客户端（`mcp.go`/`stdio.go`/`adapter.go`/`manager.go`）与 `tools.Tool` 适配。
- `internal/tui/`：交互式终端界面（bubbletea + lipgloss）。TUI 是默认交互入口，取代早期的逐行 REPL。
- 业务逻辑一律放 `internal/`；不要新建 `pkg/`，除非确实要被仓库外导入。
- 仅支持 Windows，交付就是单个 Go 二进制；不引入其它语言的脚本，构建只用 `go build`。

## 常用命令
- 构建：`go build ./...`（编译检查）；打包 exe：`go build -o bin\zcode.exe ./cmd/zcode`（仅 Windows）。
- 拉取依赖：默认 `proxy.golang.org` 常不可达，需 `GOPROXY=https://goproxy.cn,direct`。新增第三方依赖后提交 `go.mod`/`go.sum`。
- 运行（打包版）：`.\bin\zcode.exe -request "设计一个..." [-out design.md] [-mode plan|build] [-v] [-trace trace.jsonl] [-dir path] [-config path]`
- 运行（开发版）：`go run ./cmd/zcode -request "设计一个..." [-out design.md] [-v]`
- 交互式 TUI：`.\bin\zcode.exe`（不传 `-request` 且 stdin 是终端时自动进入）或 `-i`/`-tui`。快捷键 `Enter` 发送、`Ctrl+J` 换行、`Shift+Tab` 切换 Plan/Build、`Esc` 取消、`Ctrl+C` 退出；命令 `/help`、`/reset`、`/clear`、`/model`、`/plan`、`/build`、`/mode`、`/memory`、`/exit`。`-out`/`-v` 在 TUI 被忽略（用 `-trace` 记录步骤）。
- 模式默认：交互式 `build`；一次性（传了 `-request`）且未显式 `-mode` 时默认 `plan`。
- 版本：`cmd/zcode` 的 `var version`，构建用 `-ldflags "-X main.version=..."` 覆盖；`-version` 打印。
- 工具默认作用于 **exe 所在目录**（`-dir` 为空时取 `os.Executable()` 的父目录，方便双击 `bin\zcode.exe`；`go run` 的临时构建目录会被识别并回退到当前工作目录）；要指向别处传 `-dir path`。
- 可观测性：`-v` 把每步（工具、参数、结果、耗时、token）打到 stderr；`-trace <file>` 写 JSONL 逐步记录；运行结束打印 `[done] tokens(p/c/t)=...` 汇总。
- 全部测试：`go test ./...`
- 单个测试：`go test ./internal/zcode -run TestAgentRunsToolThenAnswers -v`
- 静态检查 / 格式化：`go vet ./...`、`gofmt -w .`
- 提交前顺序：`gofmt -w .` → `go vet ./...` → `go test ./...`

## 配置与优先级
- 优先级（低到高）：内置默认 → `config.json` → `LLM_*` 环境变量 → 命令行 flag。
- `config.json` 字段：`base_url`、`api_key`、`model`、`max_steps`、`timeout_seconds`、`mode`、`modes.{plan,build}.system`、`memory.{enabled,path}`、`rules.{enabled,files[]}`、`session.{enabled,dir}`、`context.{max_tokens,compact_at,keep_recent}`、`permissions.{default,tools{},commands{allow[],deny[]}}`、`mcp.{enabled,servers[]}`；缺省字段保留默认值（默认 `https://api.openai.com/v1`、`gpt-4o-mini`、`max_steps=12`、`timeout_seconds=300`、`mode=build`、`memory.enabled=true`、`memory.path=.zcode/memory.md`、`rules.enabled=true`、`rules.files=["AGENTS.md"]`、`session.enabled=true`、`session.dir=.zcode/sessions`、`context.max_tokens=0`（关闭压缩）、`context.compact_at=0.75`、`context.keep_recent=6`、`permissions.default=allow`、`mcp.enabled=false`）。
- `config.json` 含密钥，已被 `.gitignore` 忽略；模板见 `config.example.json`，不要提交真实密钥。
- `-config` 未指定时的查找顺序：**可执行文件同目录**的 `config.json` → 当前目录的 `config.json` → 内置默认。打包版因此可自包含（`bin\config.json`），开发版 `go run` 会回退到仓库根目录。
- **显式**传入 `-config` 指向不存在的文件会报错（避免拼错路径静默用默认值）。
- 新增配置项时，在 `internal/config.Config` 加字段并同步 `Normalize`/`ApplyEnv` 与 `config.example.json`。

## 环境变量（覆盖 config.json，绝不硬编码密钥）
- `LLM_API_KEY`：密钥。
- `LLM_BASE_URL`：OpenAI 兼容地址。
- `LLM_MODEL`：模型名。
- `ZCODE_MODE`：默认模式（`plan`/`build`）。
- `ZCODE_MEMORY`：`0`/`false` 关闭项目记忆，其它值开启。
- `ZCODE_RULES`：`0`/`false` 关闭项目规则注入，其它值开启。
- `ZCODE_SESSION`：`0`/`false` 关闭会话持久化，其它值开启。
- `ZCODE_MCP`：`1`/`true` 开启 MCP，其它值关闭。

## 约定与坑
- 内置工具：`list_dir`、`read_file`、`search`、`write_file`、`edit`、`run_command`；记忆工具 `remember`/`recall`/`forget`；技能工具 `skill`（仅在 `.zcode/skills` 非空时注册）；MCP 工具 `mcp__<server>__<tool>`。注册顺序即暴露给模型的顺序（`cmd/zcode`）。
- 工具返回给模型前统一经 `truncate`（默认 64 KiB，保留头尾 + 省略标记）。`read_file` 另有 8 MiB 硬读取上限，`search` 跳过二进制与 >1 MiB 文件，`run_command` 输出默认截断。
- `list_dir`/`search` 共用 `isIgnoredDir` 噪声目录名单（`.git`/`node_modules` 等）；新增遍历类工具必须复用它。
- `llm.Client` 是测试替身边界：测试禁止真实网络请求。Agent 循环用 `internal/zcode` 的 fake client 模式；HTTP 层用 `httptest`。
- 可观测性统一走 `zcode.Agent.OnEvent`（事件类型见 `zcode.EventKind`）；新增输出应写 `internal/observe` 风格的消费者，不要在 agent 循环里直接打印。token 用量由 `llm.Response.Usage` 汇总到 `Agent.Usage`。
- 多轮：`Agent.Design` 是一次性（重置 `history` 与 `Usage`）；`Agent.Ask` 保留 `history` 且 `Usage` 跨轮累加；TUI 走 `Ask`，`/reset` 调 `Agent.Reset`。
- 流式：`llm.StreamClient`（可选接口）提供 `ChatStream(ctx, req, onDelta)`；`agent` 仅在 `OnDelta != nil` 且 client 实现该接口时走流式，否则回退 `Chat`。`OpenAIClient` 用 `parseSSE` 组装文本、分片 tool_calls 与 usage。新增 client 至少实现 `Client`。
- TUI：`internal/tui` 通过 `zcode.OnEvent` 显示工具活动、`zcode.OnDelta` 显示流式文本；事件从 agent goroutine 经 `tea.Program.Send` 回到 UI 循环，不要在 agent 循环里直接打印。TUI 测试用构造的 `tea.Msg` 驱动 `model.Update`，不启动真实终端、不发真实请求。
- 新增工具：实现 `tools.Tool`（`Name`/`Description`/`Parameters`/`Run`），在 `cmd/zcode` 的 `tools.NewRegistry(...)` 注册，并补测试。
- 模式：`zcode.Mode` 为 `plan`/`build`，`Agent.Mode` 是唯一状态源（TUI 直接读写它，`SetMode` 校验）；切换模式**保留 `history`**，只换系统提示词与工具集。
- Plan/Build 是**能力过滤**而非仅提示词：`tools.PlanSafe`（`PlanSafe() bool`）未实现即视为不安全。过滤在两处同时生效——`zcode.toolSpecs()`（不暴露给模型）与 `zcode.allowed()`（不可执行，报 unknown tool）。内置读工具为 true，`write_file`/`run_command` 为 false，记忆工具为 true。
- 记忆：`internal/memory` 的 `Store` 是唯一实现；写盘用临时文件 + rename，去重按归一化文本，上限 200 条 / 32 KiB（超限丢最旧）。`zcode.MemoryProvider` 只暴露 `Render()`；每轮 `run` 开头重建 system 消息，所以新记忆与模式切换立刻生效。`.zcode/memory.md` 有意不被 `.gitignore` 忽略。
- MCP：`internal/mcp` 只做 stdio，协议 `2025-06-18`，换行分隔 JSON-RPC；`StdioClient` 的传输是 `io.ReadWriteCloser`，测试用内存管道注入假 server，不启动子进程。工具名经 `toolName` 规整到 `^[a-zA-Z0-9_-]{1,64}$`（超长加短哈希）。Plan 可见性取 `annotations.readOnlyHint` 或 server 级 `read_only`，默认 false。server 失败只收集错误，不中断启动。
- 重试：`llm.RetryClient` 包在 `OpenAIClient` 外层，仅重试 429/5xx/网络错误（`llm.StatusError.Retryable()` 判定），`context.Canceled`/`DeadlineExceeded` 不重试；流式仅在**首个 delta 之前**可重试，避免重复输出。
- 事件：模型回复若带工具调用发 `EventThought`，否则发 `EventAssistant`（最终 `EventFinal`）；`Event.Mode` 记录当轮模式，`EventLimit` 表示因超步/超时/重复调用而中止。`observe.Text` 不区分 thought/assistant，`Trace` 会带上 `mode`。
- ReAct 保险：`Agent.Timeout`（整轮 `context.WithTimeout`）、重复调用守卫（相同 `(工具,参数)` 连发超过 `RepeatLimit` 则拦截并提示，累计若干次后中止）。
- 所有外部调用（HTTP、命令执行）必须接收 `context.Context`；命令执行受 `RunCommand.Timeout` 限制，且不经过 shell（参数不会被解释）。
- 文件工具通过 `resolveUnder` 把路径限制在 `-dir` 根目录内；新增任何文件访问工具必须复用该函数，禁止绕过。
- `Parameters()` 返回 JSON Schema。工具名/参数名会直接进入模型提示，改动前先看测试。
- 可插拔能力统一走 `zcode.Options` 的接口字段并在 `New` 透传：`Rules`/`Memory`（共用 `MemoryProvider`，只暴露 `Render()`）、`Permissions`（`PermissionFunc`）+`OnPermission`、`Transcript`、`Compactor`。都可为 nil；`zcode.Agent` 上同名字段是唯一状态源。
- 权限：`Agent.checkPermission` 在重复调用守卫之后、真正执行工具之前判定；`ask` 无 `OnPermission` 时按 `deny`（非交互安全）。`EventPermission`/`EventWarning`/`EventCompact` 是新增事件，`observe.Text` 与 TUI 的 `handleEvent` 都要处理。
- 会话：消息落盘统一经 `Agent.appendMessage`（同时更新 `history` 与 `Transcript`），不要在循环里直接 `append(msgs, ...)`。`Agent.Reset` 也会清空会话；`Agent.Restore` 只载入历史，系统提示词每轮由 `refreshSystem` 重建。Windows 上 `O_APPEND` 句柄不能 `Truncate`，`session.Reset` 用「关闭 + `O_TRUNC` 重开」实现。
- 压缩：`Agent.run` 每步请求前调用 `Compactor`，仅当返回 `changed=true` 才替换历史；摘要失败只发 `EventWarning`，仍用裁剪后的历史继续。
- 自定义命令与技能的路径固定为项目根下 `.zcode/commands`、`.zcode/skills`（不可配置）。技能仅在非空时注册 `skill` 工具；自定义命令由 TUI 展开（模型不可见）。
- `cmd/zcode.run` 开头会重置 `flag.CommandLine`，因此可在测试里多次调用；`TestRunOneShot` 用 `httptest` 假 LLM 跑通 配置→Agent→工具→`-out` 全链路。
- 编辑工具 `edit`（`internal/tools/edit.go`）与 `write_file` 不同：定点替换，`old` 必须唯一匹配（除非 `replace_all`），路径同样经 `resolveUnder`，非 plan-safe。
- CI/发布在 `.github/workflows/`（仅 `windows-latest`）：CI 跑 gofmt/vet/test/build；`v*` tag 触发 release 打包 zip + SHA256。默认 `GOPROXY=https://goproxy.cn,direct`。
