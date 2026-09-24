# zcode

zcode 是一个用 Go 编写的「程序设计 Agent」（仅 Windows）。它通过工具（读写文件、执行命令）调研代码库，并产出结构化的程序设计文档或直接落地改动。界面参考 OpenCode：一个终端全屏 TUI，支持流式输出。内置 **Plan / Build 模式**、**项目记忆** 与 **MCP（stdio）** 支持。

整个项目只用 Go 实现，交付就是单个 `zcode.exe`，构建只用 `go build`。

## 构建

```powershell
go build -o bin\zcode.exe ./cmd/zcode
```

> 依赖需要能访问模块代理：默认的 `proxy.golang.org` 常不可达，若拉取失败先设置
> `$env:GOPROXY = "https://goproxy.cn,direct"`。

首次运行前准备配置（密钥不要提交）：把 `config.example.json` 复制为 `config.json`，填好 `base_url` / `api_key` / `model`。`config.json` 放在 **exe 同目录**（如 `bin\config.json`）或当前目录均可。

## 运行

交互式 TUI（推荐）：

```powershell
.\bin\zcode.exe                 # 无 -request 且是终端时自动进入 TUI
# 或显式： .\bin\zcode.exe -i
```

TUI 键位与命令：

- `Enter` 发送 · `Ctrl+J` 换行 · `Shift+Tab` 切换 Plan/Build · `PgUp/PgDn` 或滚轮滚动 · `Esc` 取消当前请求 · `Ctrl+C` 退出
- `/help` 帮助 · `/reset` 清空上下文 · `/clear` 只清屏 · `/model` 查看模型
- `/cost` 查看累计 token 用量 · `/context` 查看上下文估算与上限 · `/compact` 立即压缩上下文
- `/plan` · `/build` 切换模式 · `/mode` 查看当前模式
- `/memory` 查看项目记忆 · `/memory add <文本>` 手动记一条 · `/memory clear` 清空
- `/rules` 查看已注入的项目规则（`AGENTS.md`）
- `/session` 查看当前会话 · `/new` 开始新会话 · `/sessions` 列出历史 · `/resume <id>` 恢复会话
- `/undo` 撤销最近一次文件写入 · `/backups` 列出写入备份
- `/skills` 列出可用技能
- `/exit` 退出
- 用户自定义命令：把 `.zcode/commands/<名字>.md` 放好，即可用 `/<名字> <参数>` 触发（详见下文）

一次性执行：

```powershell
.\bin\zcode.exe -request "为在线待办事项应用设计后端" -out design.md
```

一次性运行默认进入 **Plan 模式**（只读）；加 `-mode build` 可让它直接改代码。

加 `-v` 可查看每一步调用的工具、参数、结果、耗时与 token（仅一次性模式）；加 `-trace trace.jsonl` 可保存逐步的 JSONL 追踪；运行结束会打印 `[done] tokens(p/c/t)=...` 汇总。

开发时也可直接 `go run ./cmd/zcode`。

## 命令行参数

| 参数 | 说明 |
| --- | --- |
| `-request "..."` | 一次性请求；省略且在终端中运行时进入 TUI |
| `-i` / `-tui` | 强制进入交互式 TUI |
| `-mode plan\|build` | 指定模式；一次性运行未显式指定时默认 `plan`，交互默认 `build` |
| `-dir <路径>` | 工具作用的根目录（默认 exe 所在目录） |
| `-out <文件>` | 把最终结果写入文件（一次性模式；写文件由 CLI 完成，Plan 模式也可用） |
| `-config <文件>` | 指定配置文件；不指定时优先可执行文件同目录的 `config.json` |
| `-y` | 自动批准工具权限（把 `ask` 当作 `allow`） |
| `-session <id>` | 恢复指定会话（一次性模式用 `Ask` 续聊） |
| `-continue` | 恢复最近一次会话 |
| `-v` | 逐步进度输出到 stderr（一次性模式） |
| `-trace <文件>` | 写 JSONL 逐步追踪（两种模式均可用） |
| `-version` | 打印版本并退出 |

工具默认作用于 exe 所在目录：双击 `bin\zcode.exe` 时就是 `bin\`，因此打包目录自包含；用 `go run` 开发时会自动回退到当前工作目录。要指向别处传 `-dir`。

## 模式（Plan / Build）

模式不是「提示词约束」，而是**能力过滤**：在 Plan 模式下，未声明为 plan-safe 的工具既不会出现在模型可见的工具列表里，也无法被执行（会被当成未知工具）。

- Planner 只看到 `list_dir`、`read_file`、`search` 以及记忆工具（`remember`/`recall`/`forget`）。
- Builder 能使用全部工具，包括 `write_file`、`run_command`。

`Shift+Tab` 或 `/plan`、`/build` 可随时切换，且**保留对话上下文**：可以先在 Plan 模式下调研出方案，再切到 Build 模式实施。模式对应的系统提示词可用 `modes.plan.system` / `modes.build.system` 覆盖。

## 项目记忆

记忆保存在项目内的 `.zcode/memory.md`，是可读、可手改的 markdown，每行一条：

```markdown
- (2026-09-24) [架构] TUI 使用 bubbletea
```

- 写入只能由模型显式调用 `remember`（或在 TUI 里 `/memory add`）完成，不会自动偷偷写。
- 每轮开始会把记忆注入系统提示词，所以新记住的内容下一轮即生效。
- 去重按文本归一化比较；文件上限 200 条 / 32 KiB，超出时丢弃最旧的。
- 记忆工具在 Plan 模式下也可用（它们不改动代码），因此规划阶段记下的事实，实施阶段仍能读到。

开关与路径：`memory.enabled`（默认 `true`）、`memory.path`（默认 `.zcode/memory.md`，相对项目根）。

## 项目规则（AGENTS.md）

启动时会读取项目根下的 `AGENTS.md`（可配置多个文件），把内容注入系统提示词，让模型遵循仓库约定。`rules.files` 默认 `["AGENTS.md"]`，缺失的文件会跳过，总长度上限 32 KiB。Plan 与 Build 模式都会注入；TUI 里用 `/rules` 查看。

## 精确编辑（edit）

除 `write_file`（整文件覆盖）外，Builder 还能用 `edit` 做定点替换：给出 `path` 和一组 `edits`，每条包含 `old`（必须唯一匹配，除非 `replace_all: true`）与 `new`。找不到或匹配不唯一时会报错并拒绝写入，避免误改。

## 权限（permissions）

默认 `permissions.default = "allow"`（保持历史行为）。可改为 `"ask"` / `"deny"`，也能按工具或命令细化：

```json
"permissions": {
  "default": "allow",
  "tools": { "run_command": "ask" },
  "commands": { "allow": ["go test", "gofmt"], "deny": ["rm -rf"] }
}
```

- `run_command` 先匹配 `commands.deny`，再匹配 `commands.allow`（按 `命令 参数` 前缀），最后才用工具级/默认决策。
- `ask` 会在 TUI 弹出 `y/n` 确认；非交互场景没有确认入口时按 `deny` 处理（`-y` 可自动批准）。
- 未知的决策值一律按 `deny`。

## 会话（session）

对话以 JSONL 逐条落盘到 `.zcode/sessions/<id>.jsonl`（默认开启，`session.enabled` / `session.dir` 可调）。每次运行默认开一个新会话；`-continue` 续最近一次，`-session <id>` 续指定会话；TUI 里用 `/sessions`、`/resume`、`/new` 管理。会话目录已被 `.gitignore` 忽略。

## 上下文压缩（context）

历史接近模型窗口时会自动压缩：先裁剪较旧的工具输出，再把最早的多轮对话交给模型总结成一条摘要。默认关闭，设置 `context.max_tokens`（模型窗口）后启用：

```json
"context": { "max_tokens": 32000, "compact_at": 0.75, "keep_recent": 6 }
```

token 数为估算（中文按字、英文约 4 字符 1 token），用于在硬上限前触发压缩，不用于计费。

## 写入备份与撤销（backup）

`write_file` 与 `edit` 在改动已有文件前，会把旧内容快照到 `.zcode/backups/`（默认开启，`backup.enabled` / `backup.dir` 可调）。TUI 里用 `/backups` 查看最近的快照，用 `/undo` 把最近一次改动恢复回去。新建文件没有旧内容，不会产生快照。备份目录已被 `.gitignore` 忽略，不进仓库。

## 自定义命令与技能

- **自定义命令**：`.zcode/commands/<名字>.md`。开头可选 `---` front matter（含 `description:`），正文为提示词模板，支持 `$ARGUMENTS`、`{{args}}` 和 `$1`…`$9` 占位符；没有占位符时参数追加到末尾。TUI 输入 `/<名字> <参数>` 即展开并发送，`/help` 会列出所有自定义命令。
- **技能（skills）**：`.zcode/skills/<名字>.md`，front matter 可选 `name` / `description`。技能不会一次性塞进提示词，而是注册成 `skill` 工具，模型按需加载全文（Plan 模式也可用）；`/skills` 可查看已加载的技能。

## MCP（stdio）

zcode 可作为 MCP 客户端，通过 stdio 连接用户配置的外部 MCP server，并把它们的工具以 `mcp__<server>__<tool>` 的名字注册给模型（名字会被规整为 `^[a-zA-Z0-9_-]{1,64}$`）。

```json
{
  "mcp": {
    "enabled": true,
    "servers": [
      { "name": "fs", "command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "."] }
    ]
  }
}
```

- 单个 server 用 `enabled` 开关（默认 `true`）。
- Plan 模式下，MCP 工具是否可用取决于 `readOnlyHint` 注解（`read_only: true` 可整体标记某个 server 只读）；未声明只读的一律视为可写，Plan 模式隐藏。
- 协议版本 `2025-06-18`，换行分隔 JSON-RPC；握手 `initialize` → `notifications/initialized`，随后 `tools/list`（支持游标分页）与 `tools/call`。
- server 发起的 `roots/list` 会返回项目根，`ping` 返回空对象；`sampling/createMessage` 暂不支持（返回 method not found）。
- server 启动失败只提示、不影响其它 server 与主流程。

## 配置

优先级（低到高）：内置默认 → `config.json` → `LLM_*` / `ZCODE_*` 环境变量 → 命令行 flag。

`config.json` 字段：

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `base_url` | `https://api.openai.com/v1` | OpenAI 兼容地址 |
| `api_key` | 空 | 密钥 |
| `model` | `gpt-4o-mini` | 模型名 |
| `max_steps` | `12` | 单次运行的最大模型轮数 |
| `timeout_seconds` | `300` | 单次运行的整轮超时（秒） |
| `mode` | `build` | 默认模式 |
| `modes.plan.system` / `modes.build.system` | 空 | 覆盖对应模式的系统提示词 |
| `memory.enabled` / `memory.path` | `true` / `.zcode/memory.md` | 项目记忆 |
| `rules.enabled` / `rules.files` | `true` / `["AGENTS.md"]` | 项目规则注入 |
| `session.enabled` / `session.dir` | `true` / `.zcode/sessions` | 会话持久化 |
| `context.max_tokens` / `context.compact_at` / `context.keep_recent` | `0` / `0.75` / `6` | 上下文压缩（`max_tokens=0` 关闭） |
| `backup.enabled` / `backup.dir` | `true` / `.zcode/backups` | 写入前快照与 `/undo` |
| `permissions.default` / `permissions.tools` / `permissions.commands` | `allow` / `{}` / `{}` | 工具权限 |
| `mcp.enabled` / `mcp.servers` | `false` / `[]` | MCP |

模板见 `config.example.json`。

环境变量：`LLM_API_KEY`、`LLM_BASE_URL`、`LLM_MODEL`（连接）；`ZCODE_MODE`、`ZCODE_MEMORY`、`ZCODE_RULES`、`ZCODE_SESSION`、`ZCODE_BACKUP`（`0`/`false` 关闭）、`ZCODE_MCP`（`1`/`true` 开启）。

`config.json` 已被 `.gitignore` 忽略，不要提交真实密钥。`.zcode/memory.md` **有意不忽略**：它是项目知识，应该随仓库共享。

任何 OpenAI 兼容的 `/chat/completions` 服务都可以，流式（SSE）与非流式均支持。瞬时失败（429 / 5xx / 网络错误）会自动退避重试；流式一旦已经吐出内容就不再重试，避免重复输出。

## 工具

内置：`list_dir`、`read_file`、`search`、`write_file`、`edit`、`run_command`；记忆：`remember`、`recall`、`forget`；技能：`skill`；MCP：`mcp__<server>__<tool>`。全部作用于 `-dir` 根目录内（路径经 `resolveUnder` 限制）；输出统一截断，`run_command` 受超时限制且不经过 shell。

所有工具调用都经过权限策略；`write_file`、`edit`、`run_command` 不是 plan-safe，Plan 模式下不可见也不可执行。

ReAct 循环另有几道保险：整轮超时、相同 `(工具, 参数)` 重复调用的拦截（重复若干次后中止）。

## 结构

- `cmd/zcode/` — CLI 入口（参数解析、装配、TUI 启动）
- `internal/config/` — 配置加载（JSON + 环境变量覆盖）
- `internal/llm/` — 模型后端（`Client` 接口 + OpenAI 兼容实现、流式、重试装饰器）
- `internal/zcode/` — Agent 主循环、模式与事件流（`OnEvent` / `OnDelta`，主类型 `zcode.Agent`）
- `internal/tui/` — 交互式终端界面（bubbletea + lipgloss）
- `internal/observe/` — 进度输出与 JSONL 追踪
- `internal/tools/` — 工具与注册表
- `internal/memory/` — 项目记忆存储（`.zcode/memory.md`）
- `internal/rules/` — 项目规则（`AGENTS.md`）加载与注入
- `internal/session/` — 会话持久化（JSONL）
- `internal/compact/` — 上下文压缩（裁剪 + 摘要）
- `internal/backup/` — 写入前的文件快照与恢复
- `internal/permission/` — 工具权限策略
- `internal/command/` — 自定义斜杠命令
- `internal/skill/` — 按需加载的技能
- `internal/mcp/` — MCP stdio 客户端与工具适配

## 持续集成与发布

- `.github/workflows/ci.yml`：在 `windows-latest` 上跑 `gofmt` 检查、`go vet`、`go test ./...` 与 `go build`；已设置 `GOPROXY=https://goproxy.cn,direct` 以绕过镜像可达性问题。另有一个**非阻塞**的 `-race` 任务（用 msys2 装 MinGW gcc），失败不影响主任务。
- `.github/workflows/release.yml`：推送 `v*` 标签时构建带版本号的 `zcode.exe`，打包为 `zcode-windows-amd64.zip`（含 `config.example.json`、`README.md`）并生成 SHA256 校验和，作为 GitHub Release 资产发布。

开发约定见 `AGENTS.md`。
