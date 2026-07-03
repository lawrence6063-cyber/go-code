# cogent · 自主编码 Agent 运行时（Go）

> English abstract: `cogent` (Coding aGENT in Go) is a production-grade autonomous coding agent runtime written in Go. Given a natural-language task, it autonomously explores a real codebase, plans, invokes tools to read/write files and run commands, verifies via tests, and self-corrects on failure — all streaming, interruptible, and resumable. Beyond single-turn ReAct, it ships an **outer goal-driven loop** (execute → independent verify → retry-with-feedback until an objective passes or a triple budget guardrail trips), a **maker/reviewer dual-role** pipeline with **git-worktree physical isolation** ("merge only if it passes"), a **Progressive-Disclosure skill system**, a cross-run **progress board**, and a fully hand-written **interactive TUI**. Engineering highlights: hand-written ReAct core (no heavy framework), concurrency-safe tool scheduling (read-parallel / write-serial), context-window compaction with state re-injection, append-only JSONL session resume, fail-closed security (permission / sandbox / path validation / secret redaction), OpenTelemetry-native tracing of the full ReAct decision tree, a minimal from-scratch MCP client, and isolated SubAgent dispatch. MIT licensed.

---

## 项目简介

代号 `cogent`（Coding aGENT in Go）——一个能在真实代码库里**自主干活**的生产级 Agent Runtime。

`cogent` 给定自然语言任务（"修复这个 bug / 给函数加单测 / 重构 X 为 Y"），自主完成
**探索代码 → 制定计划 → 调用工具读写文件、执行命令 → 跑测试验证 → 失败自我修正**，
全过程流式可见、可中断、可恢复。

它不止是一个单轮 ReAct Agent，而是围绕"**让 Agent 可控地长时间自主推进**"这一目标，构建了一整套工程能力：

- **目标驱动循环**（`goal`）：给定可验证的终止条件，持续"执行→独立判定→带反馈续跑"直到达标或撞预算。
- **双角色审查**（`review`）：maker 改、reviewer 独立审，通过才落盘；可选 git worktree 物理隔离。
- **守护式循环**（`loop`）：用 progress 看板判断"还在推进吗"，停滞自动退出。
- **技能系统**（`skills`）：Progressive Disclosure，按需注入领域知识，省 token。
- **完整交互式 TUI**：手写 raw 行编辑器、`@`/`/` 补全、历史反查、类 Claude Code 的折叠式流式渲染。

### 为什么用 Go

LLM 应用层生态以 Python 为主，但 Agent Runtime 的**工程难点恰好落在 Go 的强项上**：

1. **并发工具调度**：多个 `tool_use` 的并发/串行分批、超时、取消、错误传播 —— Go 的 `goroutine` + `channel` + `context.Context` + `errgroup` 是教科书级契合。
2. **流式与背压**：LLM SSE 流、工具进度流、UI 渲染流的多级 pipeline，用 channel 表达比回调/Promise 更清晰。
3. **进程与隔离**：命令执行、子进程沙箱、MCP 子进程（stdio transport）、git worktree 天然落在 `os/exec`。
4. **单二进制分发**：交叉编译、零依赖部署，适合做"开箱即用的本地 Agent"。

### 能力总览

| 分组 | 能力 | 说明 |
| --- | --- | --- |
| **执行内核** | 统一 ReAct 内核 | CLI 与自治循环共用同一个 `engine.Engine`，UI 仅消费事件 channel |
| | 工具即协议 | 读/写/编辑文件、执行命令、grep、查找、子 Agent、MCP 外部工具，全实现统一 `Tool` 接口 |
| | 并发安全调度 | 只读并发、写串行，fail-closed 默认，`errgroup` + 槽位写法防竞态 |
| | 上下文工程 | 动态窗口计算 + 阈值自动压缩（compact）+ 状态重注入 + 连续失败熔断 |
| **自主循环** | 目标驱动循环 | 执行→独立判定→带反馈续跑，直到验收通过或撞**三重预算护栏** |
| | 独立验收判定 | 验收脚本（退出码 0 = 达成）与执行体解耦，**执行体无法篡改判定**，fail-closed |
| | maker/reviewer | 实现者改 + 独立审查者审，通过才落盘；客观 verify 为硬闸门 |
| | worktree 隔离 | git worktree 独立目录 + 临时分支，通过才 Merge、否则 Discard，物理隔离 |
| | 守护式循环 | progress 快照判断推进，停滞退出，适合长时盯守 |
| | 待办看板 | 跨 run 的 `.cogent/progress.md`（todo/doing/done/blocked）|
| **安全** | 权限三态 + HITL | allow / ask / deny，中断点人类介入（Approve / Always / Edit / Reject）|
| | 命令沙箱 | 危险命令拦截、白名单 git、工作目录约束、路径越界防护 |
| | 密钥治理 | 密钥仅 env，落盘前对 `sk-*`/token/secret 等脱敏 |
| **扩展** | 技能系统 | Progressive Disclosure：`.cogent/skills/<name>/SKILL.md` 三级渐进披露 |
| | MCP 互操作 | 自实现最小 stdio MCP client，`mcp__<server>__<tool>` 命名隔离 + 内建优先 |
| | SubAgent 派发 | 隔离上下文执行探索类子任务，结果摘要回流主循环 |
| **可观测** | 全链路 Trace | OpenTelemetry 把 ReAct 循环建成 span 树，默认 file exporter，可一键切 Jaeger |
| | 成本计量 | 拦截 token 计数累计美元成本，接入 `--max-cost` 护栏 + 状态栏展示 |
| **交互** | 运行档位 | Plan（只读探索+产计划）/ Ask（只读问答）/ Auto（默认，可写）|
| | 交互式 TUI | 手写 raw 行编辑器、`@`/`/` 补全、`Ctrl-R` 历史反查、折叠式流式渲染、常驻状态栏 |

---

## 快速开始

```bash
# 1. 配置密钥（仅从环境变量读取，严禁硬编码）
export DEEPSEEK_API_KEY=sk-xxxx
# 可选：自定义 OpenAI 兼容 BaseURL（默认 https://api.deepseek.com/v1）
export COGENT_LLM_BASE_URL=https://api.deepseek.com/v1
# 可选：指定模型（默认 deepseek-chat）
export COGENT_MODEL=deepseek-chat

# 2. 进入交互式对话（默认 auto 档位，可自主读写）
go run ./cmd/cogent run
# 或携带首轮任务
go run ./cmd/cogent run "给 internal/foo 的 Bar 函数加一行日志"

# 3. 目标驱动循环：跑到验收脚本通过为止（见下文"工作模式"）
go run ./cmd/cogent goal "修复 bug 并让全部测试通过" --verify ./verify.sh --max-iterations 5

# 4. 双角色审查：实现者改 + 独立审查者审，通过才落盘
go run ./cmd/cogent review "重构 X 为 Y" --worktree
```

进入 REPL 后：在 `you>` 输入任务，模型自主调用工具完成；输入 `exit`/`quit` 或按 `Ctrl-C` 退出。

> 首次运行可执行 `go run ./cmd/cogent init` 在当前目录生成 `.cogent/` 配置骨架（`config.yaml` / `mcp.json` / `skills/`）。

---

## 命令参考

`cogent` 提供 8 个子命令：

| 命令 | 作用 | 关键 flag（默认值） |
| --- | --- | --- |
| `run [task]` | 进入交互式 REPL（可带首轮任务） | `--mode`(auto)、`--max-steps`(0=引擎默认) |
| `resume <id>` | 从 JSONL 无损恢复会话续跑 | — |
| `goal <intent>` | 目标驱动循环：迭代到验收通过或撞预算 | `--verify`(脚本路径)、`--review`、`--worktree`、`--allow-dirty`、`--max-iterations`(0→默认8)、`--max-cost`(0→不限)、`--max-wallclock`、`--max-steps`、`--mode` |
| `loop <intent>` | 守护式长时循环：progress 停滞则退出 | `--interval`、`--max-iterations`、`--max-cost`、`--max-wallclock`、`--max-steps`、`--mode` |
| `review <task>` | 单轮 maker/reviewer：通过才落盘 | `--worktree`、`--mode`、`--max-steps` |
| `mcp` | 自检：连接并列出所有 MCP 外部工具 | — |
| `init` | 初始化 `.cogent/` 配置骨架 | — |
| `skills` | 列出 `.cogent/skills` 下已注册的技能 | — |

运行档位（`--mode`）：`plan`（只读探索 + 产出计划）/ `ask`（只读问答）/ `auto`（默认，可写）。

---

## 工作模式详解

### 1. 交互对话（`run` / `resume`）

统一 ReAct 内核驱动的多轮对话，配套一套手写的交互式 TUI：

- **手写 raw 模式行编辑器**：跨 darwin/linux termios，光标移动、行内编辑、CJK 宽字符对齐；状态机与 I/O 解耦，可纯逻辑单测。
- **`@` 文件补全**：`git ls-files` 优先（回退目录遍历）+ 5s TTL 缓存 + 模糊排序，避免每次击键 spawn 进程。
- **`/` 斜杠命令补全** 与 **`Ctrl-R` reverse-i-search 历史反查**。
- **类 Claude Code 流式渲染**：工具调用折叠为单行摘要（`● 工具名 参数 … ✓/✗ 行数`），正文与工具视觉分区，不再全量刷屏；子 Agent 事件收敛为摘要；非 TTY 环境回退可管道消费的纯文本。
- **常驻状态栏**：实时展示 token / 累计成本 / 模型。

### 2. 目标驱动循环（`goal`）

把 engine 的"单次执行"接成外层自治循环：**执行一轮 → 独立判定 → 不达标带反馈续跑**，直到验收脚本通过或撞预算护栏。

```bash
# verify.sh：开发者提供的"可信控制面"，退出码 0 视为目标达成
cat > verify.sh <<'EOF'
#!/usr/bin/env bash
set -e
go build ./...
go test ./internal/foo/...
EOF
chmod +x verify.sh

go run ./cmd/cogent goal "修复 foo 包的空指针 bug 并保证测试通过" \
  --verify ./verify.sh \
  --max-iterations 5 --max-cost 2 --max-wallclock 10m
```

- **独立判定不变量**：验收判定器（`verify` 包）与写代码的 Agent 解耦，**执行体无法篡改判定结果**；判定过程异常一律 fail-closed 视为未通过。
- **三重预算护栏**（`loop.Budget`）：`--max-iterations` / `--max-cost`（美元）/ `--max-wallclock`，任一触顶即停。**未显式设置也会用保守默认（8 轮 / $5 / 15 分钟），绝不无限烧钱。**
- **结局归因**：`achieved`（唯一成功）/ `budget-spent` / `canceled` / `fatal`，终局打印迭代数、耗时与最后一次判定摘要。
- 加 `--review` / `--worktree` 可让每轮用双角色 + 隔离落盘（见下）。

### 3. 双角色审查与隔离落盘（`review`，或 `goal --review/--worktree`）

```bash
go run ./cmd/cogent review "把 internal/foo 的同步实现改为并发安全" --worktree
```

- **maker/reviewer**：maker 实现改动，reviewer 独立审查并给裁决/反馈。
- **落盘闸门（两级）**：提供 `--verify` 时以**客观 verify 为硬闸门**（reviewer 降级为建议）；否则回退 reviewer 裁决。
- **worktree 物理隔离**：`--worktree` 时改动发生在独立 git worktree（临时分支 `cogent/wt-*`），**审查通过才 Merge 回基线，否则 Discard 清理**；比"写了再回滚"的 diff 暂存更干净，天然支持多 maker 并行。
- **安全**：所有 git 操作经沙箱（白名单命令 + 危险拦截 + 目录约束），模型工具不得直接执行 `git worktree`。脏工作树会被前置拦截（`--allow-dirty` 可跳过，风险自负），合并冲突交人介入。

### 4. 守护式循环（`loop`）

与 `goal` 的区别：`goal` 需要可验证终止条件；`loop` 没有验收脚本，用 **progress 快照**判断"是否还在推进"，停滞则退出——适合"持续盯着某目标干活"的守护场景。

```bash
go run ./cmd/cogent loop "逐步补齐 internal 各包的单测覆盖" --interval 30s --max-iterations 20
```

- 跨 run 待办看板落盘 `.cogent/progress.md`（人类可读 Markdown，状态 todo/doing/done/blocked），是 Loop 的"仓库记忆"，与 session（单任务恢复）、memory（长期约定）职责正交。

---

## 内建工具

| 工具 | 作用 | 只读/并发安全 | 权限 |
| --- | --- | --- | --- |
| `read_file` | 读取工作区文件 | 是 | 自动放行 |
| `list_dir` | 列出目录条目 | 是 | 自动放行 |
| `grep` | 正则检索文件内容 | 是 | 自动放行 |
| `find_files` | 按名/glob 查找文件 | 是 | 自动放行 |
| `write_file` | 创建/覆盖文件 | 否 | ask（HITL）|
| `edit_file` | 按旧串精确替换 | 否 | ask（HITL）|
| `bash` | 执行 shell 命令（沙箱）| 否 | ask；危险命令直接拒 |
| `task` | 派发独立上下文 SubAgent 执行探索类子任务 | —（受限只读工具面）| — |

### 人在环（HITL）

写/执行类工具默认在中断点暂停征询：

```
[permission] tool "write_file" — path=main.go (content 1.2KB)
  approve / always / edit / reject? [a/A/e/r]
```

- `a` 批准原样执行；`A`（大写）或 `always` 批准并设置该工具**会话级自动放行**（后续同工具不再询问，exit 清除）；`e` 输入修正后的 JSON 入参再执行；`r` 拒绝并可附指引让模型改道。
- 权限提示只展示**入参摘要**（工具名 + 关键参数），不再刷完整 JSON。
- 危险命令（`rm -rf /` 等）与控制面写入（`.cogent/`、`.git/`）在到达审批前就被硬拦，always 不会绕过。

### 安全边界

- 所有文件操作经路径校验，`../` 越界被拒；`.cogent/`、`.git/` 等控制面写入被禁止（progress/memory 等只许经受控通道写）。
- `bash` 拦截破坏性命令（`rm -rf /`、`curl ... | sh`、fork bomb 等），默认 ask。
- 密钥仅来自环境变量；落盘前对疑似密钥（`sk-`、`api_key`/`token`/`secret`/`password` 字段）脱敏。

---

## 会话持久化与恢复

每次 `run`/`resume` 都对应一个会话，所有交互（用户任务、助手回复含工具调用、工具结果）以
**append-only JSONL 事件流**落盘到 `./data/<session-id>.jsonl`（`.gitignore` 已排除）。
任务中途被 `Ctrl-C` 中断或进程崩溃后，可无损接着干：

```bash
go run ./cmd/cogent run "重构 internal/foo 模块"
# cogent — session 20260617-171500-xxxx — ...
#   (resume later with: cogent resume 20260617-171500-xxxx)

go run ./cmd/cogent resume 20260617-171500-xxxx
```

- 写入路径极简（顺序 append、崩溃安全）；恢复路径承担去重与配对修复（剥离悬空 `tool_use`、丢弃孤立 `tool_result`）。
- 落盘前脱敏；session-id 经字符白名单校验防路径穿越；文件 `0o600`、目录 `0o700`。

---

## 技能系统（Skills）

受 Claude Code Skills 启发的 **Progressive Disclosure** 技能系统：把领域知识/工作流沉淀为可复用技能，**按需加载、节省 token**。

技能 = `.cogent/skills/<name>/SKILL.md`（YAML frontmatter: `name` / `description` + Markdown 正文）：

```
.cogent/skills/
└── pdf-extract/
    ├── SKILL.md        # frontmatter(name/description) + 正文
    └── extract.py      # 正文可引用的附加脚本/模板
```

**三级渐进披露**：① 启动仅注入 `name`+`description`（省 token）；② 模型判断相关后调 skill 工具读正文；③ 正文再按需引用同目录附加文件。

```bash
go run ./cmd/cogent skills   # 列出当前工作区已注册的技能
```

---

## 接入 MCP 外部工具

cogent 内建一个**最小手写的 MCP（Model Context Protocol）stdio 客户端**，通过 `os/exec`
拉起任意 MCP server 子进程并以换行分隔 JSON-RPC 2.0 通信，把远端工具以 `mcp__<server>__<tool>`
命名、**fail-closed** 融入工具池（内建优先去重、外部统一过 Guard/HITL）。
主模块**零新增第三方依赖**，不引入官方 SDK：MCP 协议为亲手实现，并通过内联的协议一致性套件 + 兼容 fake server 验证。

在工作根目录创建 `.cogent/mcp.json`（缺省时 cogent 仍可独立运行，MCP 为可插拔增强）：

```json
{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp/cogent-sandbox"]
    },
    "fetch": { "command": "uvx", "args": ["mcp-server-fetch"] }
  }
}
```

```bash
go run ./cmd/cogent mcp                              # 自检：连接并列出外部工具
go run ./cmd/cogent run "用 fetch 工具抓取 https://example.com 并总结"
```

- **命名隔离**：MCP 工具一律带 `mcp__<server>__<tool>` 前缀，物理上不会冒用内建工具名。
- **内建优先**：工具池先到先得，内建排前，同名冲突内建必胜。
- **安全默认**：MCP 工具默认非并发安全、权限 `ask`，统一经 `Guard` 走 HITL。
- **子进程回收**：`defer mgr.Close()` 关闭 stdin → 等待 → 超时强杀，绝不悬挂。

---

## SubAgent 派发

对"在大仓里定位某功能实现"这类**探索类子任务**，cogent 提供内建 `task` 工具：主 Agent 把
子任务交给一个**独立上下文的子 Agent** 执行，子 Agent 跑完只把**结果摘要**回流主循环，从而隔离
大量中间消息、保持主上下文干净。

```text
主 Agent ──task(prompt)──▶ 子 Agent（独立 msgs / 受限只读工具池 / 不落盘）
       ◀── 结果摘要(≤8KB) ── 探索 read_file/list_dir/grep 后产出摘要
```

- **上下文隔离**：每次派发新建独立子 Engine，强制 `Session=nil` 不写主会话 transcript。
- **受限工具面**：子 Agent 只装配只读工具，**且不含 `task` 自身**，从工具面杜绝无限递归派发。
- **失控护栏**：子任务 `MaxSteps` 比主循环更紧；摘要回流截断到 8KB。
- **trace 串联**：子 Agent 执行作为父任务 trace 的 `agent.spawn` 子 span 挂接。
- **依赖破环**：`Spawner` 接口定义在 `tool` 包，`agent` 仅 import `engine`，保证 `cmd → agent → engine → tool → types` 无循环依赖。

---

## 可观测 Trace

cogent 用 **OpenTelemetry SDK** 把一次任务的 ReAct 决策链建成一棵 span 树，回答"慢/贵/错在哪"。
默认关闭（零开销 no-op），显式开启后可落本地文件或推到 Jaeger 看火焰图。

```text
cogent.session
└── react.step
    ├── llm.stream                # model / tokens / 首字延迟 / finish_reason
    └── tool.batch
        └── tool.call             # tool.name / is_error / duration
            ├── permission.check  # allow/ask/deny
            └── agent.spawn       # SubAgent 子树（经 ctx 自动挂接）
```

| 环境变量 | 含义 | 默认 |
| --- | --- | --- |
| `COGENT_OBSERVE_ENABLED` | 总开关 | `false`（零开销 no-op） |
| `COGENT_TRACE_EXPORTER` | `file` \| `stdout` \| `otlp` \| `none` | `file` |
| `COGENT_TRACE_DIR` | file exporter 输出目录 | `./data/traces` |
| `COGENT_OTLP_ENDPOINT` | otlp 的 gRPC 地址 | `localhost:4317` |
| `COGENT_TRACE_SAMPLE_RATIO` | 采样率 0.0~1.0 | `1.0` |

```bash
# 本地文件（零依赖，jq 可查）
COGENT_OBSERVE_ENABLED=true COGENT_TRACE_EXPORTER=file go run ./cmd/cogent run "给 X 加一行日志"
jq -r '.Name' data/traces/traces-*.jsonl

# 接 Jaeger 看火焰图
docker run -d --name jaeger -p 16686:16686 -p 4317:4317 jaegertracing/all-in-one:latest
COGENT_OBSERVE_ENABLED=true COGENT_TRACE_EXPORTER=otlp COGENT_OTLP_ENDPOINT=localhost:4317 \
  go run ./cmd/cogent run "修复这个 bug 并跑测试"
# 打开 http://localhost:16686 ，选择 service=cogent
```

- 结构化日志（slog）自动注入当前 `trace_id`/`span_id`，日志可与 span 互相对照。
- 成本计量：拦截 `cogent.tokens` 计数累计美元成本，既喂给状态栏展示，也接入 `--max-cost` 护栏。

---

## 架构概览

```text
形态层 cmd/cogent          CLI 子命令 run/resume/goal/loop/review/mcp/init/skills · 事件渲染装配
       │
交互层 internal/tui         行编辑器/补全/历史/流式渲染/状态栏/菜单/HITL（含 render·completion·history 子包）
       │
自治层 internal/loop        目标驱动外层循环（执行→判定→续跑 · 三重预算护栏）
  ├── verify                独立验收判定（脚本 · fail-closed · 执行体不可篡改）
  ├── worktree              git worktree 物理隔离（通过才 Merge）
  ├── agent                 maker/reviewer 双角色 · SubAgent 派发
  └── progress              跨 run 待办看板 .cogent/progress.md
       │
执行内核 internal/engine    ReAct 主循环（单一真相源，CLI + 自治循环共用）
       │
能力层 ── llm               DeepSeek OpenAI 兼容 + SSE 流式
  ├── tool / orchestrate    工具协议 · 并发分批调度
  ├── permission / sandbox  权限三态 · 命令沙箱 · 路径校验
  ├── contextmgr / memory   窗口/compact · MEMORY.md
  ├── skills / config       技能系统 · .cogent/config.yaml
  ├── session / secret      JSONL 事件流 · resume · 落盘脱敏
  └── mcp                   最小 stdio MCP client
       │
可观测 observe              Tracer · Meter · slog（横切，no-op 降级）
       │
共享类型 types              Message / ToolUseBlock / StreamEvent（最内层，不依赖业务包）
```

- **依赖方向**：一律向内，`cmd → {tui, loop, engine, ...} → types`；`loop → engine` 单向、engine 零反向依赖；各层仅依赖 `observe` 薄接口与 `types` 共享类型，杜绝循环依赖。
- **不变量**：单一真相源、ctx 一条到底（取消 + trace）、事件单向上抛、工具池运行期只读、function calling 配对完整、独立判定、预算先行、fail-closed。

> 详细的架构设计、模块接口与关键机制请参阅 [DEV_SPEC.md](spec/DEV_SPEC.md) 与 [LOOP_SPEC.md](spec/LOOP_SPEC.md)、[OPTIMIZE_SPEC.md](spec/OPTIMIZE_SPEC.md)。

---

## 配置说明

配置项与环境变量互补：**env 优先级更高**（12-factor），`.cogent/config.yaml` 提供项目级默认。

| 环境变量 | 含义 | 默认 |
| --- | --- | --- |
| `DEEPSEEK_API_KEY` | DeepSeek API 密钥（必需） | — |
| `COGENT_LLM_BASE_URL` | OpenAI 兼容 BaseURL | `https://api.deepseek.com/v1` |
| `COGENT_MODEL` | 模型名 | `deepseek-chat` |
| `COGENT_MAX_REACT_STEPS` | 单轮 ReAct 最大步数 | `50` |
| `COGENT_OBSERVE_ENABLED` | 可观测总开关 | `false` |
| `COGENT_TRACE_EXPORTER` | trace 导出后端 | `file` |
| `COGENT_LOG_LEVEL` | 日志级别 | `info` |

密钥仅从环境变量读取，代码与配置文件中严禁硬编码；`.env` 已被 `.gitignore` 排除。

---

## 深入阅读

`docs/` 下提供了一批可离线打开的 HTML 深度教程（按主题）：

| 主题 | 文档 |
| --- | --- |
| ReAct 主循环深挖 | `docs/agent-loop-deep-dive.html` |
| 引擎事件流 | `docs/engine-eventflow-tutorial.html` |
| 目标/守护循环 | `docs/loop-goal-tutorial.html` |
| 上下文压缩 | `docs/context-compaction-tutorial.html` |
| 会话恢复 | `docs/session-resume-tutorial.html` |
| 权限/沙箱/HITL | `docs/permission-sandbox-hitl-tutorial.html` |
| MCP 与 SubAgent | `docs/mcp-subagent-tutorial.html` |
| 可观测 Trace | `docs/observability-trace-tutorial.html` |
| 交互式 TUI 全功能 | `docs/tui-complete-guide.html` |
| 数据流深挖 | `docs/dataflow-deep-dive.html` |

---

## 开发

```bash
export GOTOOLCHAIN=auto   # go.mod 要求 go 1.26，缺失时自动下载对应工具链
go build ./...
go test -race ./...       # 单元 + 组件测试（含 goroutine 泄漏检测）
```

代码规模：约 **216 个 Go 文件**、**84 个测试文件**、生产代码约 **3.6 万行**、测试约 **1.7 万行**。

### 目录结构

```text
cogent/
├── cmd/cogent/                 # CLI 入口：run/resume/goal/loop/review/mcp/init/skills
├── internal/
│   ├── types/                  # Message / ToolUseBlock / StreamEvent（最内层）
│   ├── engine/                 # ReAct 主循环（执行内核）
│   ├── tui/                    # 交互式终端 UI（+ render/completion/history 子包）
│   ├── loop/                   # 目标驱动外层自治循环 + 预算护栏
│   ├── verify/                 # 独立验收判定器
│   ├── worktree/               # git worktree 物理隔离
│   ├── agent/                  # maker/reviewer 双角色 + SubAgent 派发
│   ├── progress/               # 跨 run 待办看板
│   ├── skills/                 # Progressive Disclosure 技能系统
│   ├── llm/                    # DeepSeek 兼容客户端 + SSE
│   ├── tool/                   # Tool 接口 + Pool + 8 内建工具 + Guard
│   ├── orchestrate/            # 并发/串行分批调度
│   ├── permission/             # 权限三态 + HITL
│   ├── sandbox/                # 命令沙箱 + 路径校验
│   ├── contextmgr/             # 窗口计算 + auto-compact
│   ├── memory/                 # MEMORY.md 加载
│   ├── session/                # JSONL 事件流 + resume
│   ├── secret/                 # 落盘脱敏
│   ├── config/                 # .cogent/config.yaml 加载
│   ├── mcp/                    # MCP client（stdio）
│   └── observe/                # OTel 封装：Tracer/Meter/Provider
├── eval/                       # 评测集与跑分脚本
├── data/                       # 运行期数据（.gitignore 排除）
├── spec/                       # DEV_SPEC / LOOP_SPEC / OPTIMIZE_SPEC
└── docs/                       # 技术教程（HTML）
```

### 关键依赖

| 用途 | 选型 |
| --- | --- |
| CLI | `github.com/spf13/cobra` |
| LLM 客户端 | `github.com/sashabaranov/go-openai` |
| 并发 | `golang.org/x/sync/errgroup` |
| 系统调用（termios）| `golang.org/x/sys` |
| MCP | 自实现最小 stdio 客户端（不引入第三方 SDK） |
| YAML（frontmatter/config）| 自实现极简子集解析（不引第三方） |
| 日志 | `log/slog`（标准库） |
| 可观测 | `go.opentelemetry.io/otel`（+ SDK / OTLP exporter） |
| 测试 | `go.uber.org/goleak` |

---

## Contributing

欢迎通过 Issue 提交问题或建议，通过 Pull Request 贡献代码。提交前请确保：

- `gofmt` / `goimports` 无差异，`go vet` 无告警
- `go test -race ./...` 全绿
- 遵循 Go 工程规范：error 末位且必处理、禁用 panic 作常规错误流、导出符号带注释、函数 < 80 行、嵌套 < 4 层、参数 ≤ 5

---

## License

MIT License. See [LICENSE](LICENSE).
