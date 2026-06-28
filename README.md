# cogent · 自主编码 Agent 运行时（Go）

> English abstract: `cogent` (Coding aGENT in Go) is a production-grade autonomous coding agent runtime written in Go. Given a natural-language task, it autonomously explores a real codebase, formulates a plan, invokes tools to read/write files and run commands, executes tests for verification, and self-corrects on failure — all streaming, interruptible, and resumable. Highlights: hand-written ReAct core (no heavy framework), concurrency-safe tool scheduling (read-parallel / write-serial), context-window compaction with state re-injection, append-only JSONL session resume, fail-closed security (permission / sandbox / path validation), OpenTelemetry-native tracing of the full ReAct decision tree, MCP client interop, and SubAgent dispatch. MIT licensed.

---

## 项目简介

代号 `cogent`（Coding aGENT in Go）——一个能在真实代码库里自主干活的生产级 Agent Runtime。

`cogent` 给定自然语言任务（"修复这个 bug / 给函数加单测 / 重构 X 为 Y"），自主完成
**探索代码 → 制定计划 → 调用工具读写文件、执行命令 → 跑测试验证 → 失败自我修正**，
全过程流式可见、可中断、可恢复。

### 为什么用 Go

LLM 应用层生态以 Python 为主，但 Agent Runtime 的**工程难点恰好落在 Go 的强项上**：

1. **并发工具调度**：多个 `tool_use` 的并发/串行分批、超时、取消、错误传播 —— Go 的 `goroutine` + `channel` + `context.Context` + `errgroup` 是教科书级契合。
2. **流式与背压**：LLM SSE 流、工具进度流、UI 渲染流的多级 pipeline，用 channel 表达比回调/Promise 更清晰。
3. **进程与隔离**：命令执行、子进程沙箱、MCP 子进程（stdio transport）天然落在 `os/exec`。
4. **单二进制分发**：交叉编译、零依赖部署，适合做"开箱即用的本地 Agent"。

### 核心特性

| 特性 | 说明 |
| --- | --- |
| 统一执行内核 | CLI 与 Headless 共用同一个 `engine.Engine`，UI 仅消费事件 channel |
| 工具即协议 | 读/写/编辑文件、执行命令、grep、子 Agent、MCP 外部工具，全部实现统一 `Tool` 接口 |
| 并发安全调度 | 只读并发、写串行，fail-closed 默认值，`errgroup` + 槽位写法防竞态 |
| 上下文工程 | 动态窗口计算 + 阈值自动压缩 + 状态重注入 + 连续失败熔断 |
| 分层 Memory | `MEMORY.md` 入口 + 项目级 topic 文件，行/字节硬截断 |
| 可中断可恢复 | append-only JSONL 事件流落盘，`Ctrl-C` 中断、`cogent resume` 无损接续 |
| 安全可插拔 | 权限三态（allow/ask/deny）、命令沙箱、路径越界防护、密钥仅 env |
| MCP 互操作 | 自实现最小 stdio MCP client，`mcp__<server>__<tool>` 命名隔离 + 内建优先 |
| SubAgent 派发 | 隔离上下文执行子任务，结果摘要回流主循环 |
| 全链路可观测 | OpenTelemetry 把 ReAct 循环建成 span 树，默认 file exporter，可一键切 Jaeger |
| 运行模式 + 人在环 | Plan / Ask / Auto 档位 + 中断点人类介入（Approve / Edit / Reject） |

---

## 快速开始

当前已交付：DeepSeek LLM 接入 + 工具化 ReAct 主循环 + function calling + 权限三态与人在环（HITL）+ 交互式 REPL。
cogent 现在能在真实仓库里读文件、检索、改文件、跑命令，并把结果回流给模型自我验证。

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

# 运行档位：plan（只读探索+产计划）/ ask（只读问答）/ auto（默认，可写）
go run ./cmd/cogent run --mode=plan "梳理一下项目结构并给出改造计划"
```

进入 REPL 后：在 `you>` 输入任务，模型自主调用工具完成；输入 `exit`/`quit` 或按 `Ctrl-C` 退出。

### 内建工具

| 工具 | 作用 | 只读/并发安全 | 权限 |
| --- | --- | --- | --- |
| `read_file` | 读取工作区文件 | 是 | 自动放行 |
| `list_dir` | 列出目录条目 | 是 | 自动放行 |
| `grep` | 正则检索文件内容 | 是 | 自动放行 |
| `write_file` | 创建/覆盖文件 | 否 | ask（HITL）|
| `edit_file` | 按旧串精确替换 | 否 | ask（HITL）|
| `bash` | 执行 shell 命令（最小安全版）| 否 | ask；危险命令直接拒 |

### 人在环（HITL）

写/执行类工具默认在中断点暂停征询：

```
[permission] tool "write_file" requests execution:
  input: {"path":"main.go","content":"..."}
  approve / always / edit / reject? [a/A/e/r]
```

- `a` 批准原样执行；`A`（大写）或 `always` 批准并设置该工具**会话级自动放行**（后续同工具不再询问，exit 清除）；`e` 输入修正后的 JSON 入参再执行；`r` 拒绝并可附指引让模型改道。
- 危险命令（`rm -rf /` 等）与控制面写入（`.cogent/`、`.git/`）在到达审批前就被硬拦，always 不会绕过。

### 安全边界

- 所有文件操作经路径校验，`../` 越界被拒；`.cogent/`、`.git/` 等控制面写入被禁止。
- `bash` 拦截破坏性命令（`rm -rf /`、`curl ... | sh`、fork bomb 等），默认 ask。
- 密钥仅来自环境变量；完整命令沙箱与并发分批调度已落地。

---

## 会话持久化与恢复

每次 `run`/`resume` 都对应一个会话，所有交互（用户任务、助手回复含工具调用、工具结果）以
**append-only JSONL 事件流**落盘到 `./data/<session-id>.jsonl`（`.gitignore` 已排除）。
任务中途被 `Ctrl-C` 中断或进程崩溃后，可无损接着干：

```bash
# 启动时会打印当前 session-id，并提示如何恢复
go run ./cmd/cogent run "重构 internal/foo 模块"
# cogent — session 20260617-171500-xxxx — ...
#   (resume later with: cogent resume 20260617-171500-xxxx)

# 中断后从该会话恢复（重建上下文 + 修复 function calling 配对 + 续跑）
go run ./cmd/cogent resume 20260617-171500-xxxx
```

- 写入路径极简（顺序 append、崩溃安全）；恢复路径承担去重与配对修复（剥离悬空 `tool_use`、丢弃孤立 `tool_result`）。
- 落盘前对疑似密钥（`sk-`、`api_key`/`token`/`secret`/`password` 字段）脱敏；session-id 经字符白名单校验防路径穿越；文件 `0o600`、目录 `0o700`。

---

## 接入 MCP 外部工具

cogent 内建一个**最小手写的 MCP（Model Context Protocol）stdio 客户端**，通过 `os/exec`
拉起任意 MCP server 子进程并以换行分隔 JSON-RPC 2.0 通信，把远端工具以 `mcp__<server>__<tool>`
命名、**fail-closed** 融入工具池（内建优先去重、外部统一过 Guard/HITL）。
主模块**零新增第三方依赖**，不引入官方 `modelcontextprotocol/go-sdk`：MCP 协议为亲手实现，
并通过内联的协议一致性套件 + 协议兼容 fake server 验证其正确性。

在工作根目录创建 `.cogent/mcp.json`（缺省时 cogent 仍可独立运行，MCP 为可插拔增强）：

```json
{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp/cogent-sandbox"]
    },
    "fetch": {
      "command": "uvx",
      "args": ["mcp-server-fetch"]
    }
  }
}
```

```bash
# 自检：连接并列出所有可用的外部工具
go run ./cmd/cogent mcp

# 正常 run/resume 时会自动装配 MCP 工具并在退出时回收子进程
go run ./cmd/cogent run "用 fetch 工具抓取 https://example.com 并总结"
```

- 命名隔离：MCP 工具一律带 `mcp__<server>__<tool>` 前缀，物理上不会冒用内建工具名（如 `bash`）。
- 内建优先：工具池装配先到先得，内建排前、MCP 排后，同名冲突时内建必胜。
- 安全默认：MCP 工具默认非并发安全、权限 `ask`，统一经 `Guard` 装饰器走 HITL 询问。
- 子进程回收：`defer mgr.Close()` 关闭 stdin → 等待 → 超时强杀，绝不悬挂。

---

## SubAgent 派发

对"在大仓里定位某功能实现"这类**探索类子任务**，cogent 提供内建的 `task` 工具：主 Agent 把
子任务交给一个**独立上下文的子 Agent** 执行，子 Agent 跑完只把**结果摘要**回流主循环，从而
隔离大量中间消息、保持主上下文干净（DEV_SPEC §3.9、§6.7）。

```text
主 Agent ──task(prompt)──▶ 子 Agent（独立 msgs / 受限只读工具池 / 不落盘）
       ◀── 结果摘要(≤8KB) ── 探索 read_file/list_dir/grep 后产出摘要
```

- **上下文隔离**：每次派发新建独立子 Engine（独立消息历史），强制 `Session=nil` 不写主会话 transcript。
- **受限工具面**：子 Agent 只装配只读工具（`read_file`/`list_dir`/`grep`），**且不含 `task` 自身**，从工具面杜绝无限递归派发。
- **失控护栏**：子任务 `MaxSteps` 比主循环更紧（默认 16 轮）；摘要回流截断到 8KB。
- **trace 串联**：子 Agent 的执行作为父任务 trace 的 `agent.spawn` 子 span 挂接（复用 ctx 传播），可视化"主 Agent 把什么子任务派给了谁、花了多少"。
- **依赖破环**：`Spawner` 接口定义在 `tool` 包、`task` 工具仅依赖该抽象；`internal/agent` 仅 import `engine` 并返回隐式满足该接口的 `*SubAgent`，保证 `cmd → agent → engine → tool → types` 无循环依赖。

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

**本地文件（零依赖，jq 可查）：**

```bash
COGENT_OBSERVE_ENABLED=true COGENT_TRACE_EXPORTER=file go run ./cmd/cogent run "给 X 加一行日志"
jq -r '.Name' data/traces/traces-*.jsonl   # 查看产生的 span 名
```

**接 Jaeger 看火焰图（OTLP）：**

```bash
# 1) 起一个本地 Jaeger（同时开放 UI 16686 与 OTLP gRPC 4317）
docker run -d --name jaeger -p 16686:16686 -p 4317:4317 jaegertracing/all-in-one:latest

# 2) 让 cogent 把 trace 推到 Jaeger
COGENT_OBSERVE_ENABLED=true COGENT_TRACE_EXPORTER=otlp COGENT_OTLP_ENDPOINT=localhost:4317 \
  go run ./cmd/cogent run "修复这个 bug 并跑测试"

# 3) 打开 http://localhost:16686 ，选择 service=cogent 查看 span 树/火焰图
```

- 结构化日志（slog）自动注入当前 `trace_id`/`span_id`，日志可与 span 互相对照。
- 关闭时（默认）返回 no-op 实现，调用点零开销、无 `if` 分支污染内核。

---

## 架构概览

```text
形态层 cmd/cogent          CLI 子命令分流 run/resume/mcp · 事件渲染
       │
执行内核 internal/engine    ReAct 主循环（单一真相源，CLI+Headless 共用）
       │
能力层 ── llm               DeepSeek OpenAI 兼容 + SSE 流式
  ├── tool / orchestrate    工具协议 · 并发分批调度
  ├── permission / sandbox  权限三态 · 命令沙箱 · 路径校验
  ├── contextmgr / memory   窗口/compact · MEMORY.md
  └── session               JSONL 事件流 · resume
       │
扩展层 mcp / agent          MCP client · SubAgent 派发
       │
可观测 observe              Tracer · Meter · slog（横切，no-op 降级）
```

- **依赖方向**：`cmd → engine → {llm, tool, orchestrate, contextmgr, memory, session} → types`，所有层仅依赖 `observe` 的薄接口与 `types` 共享类型，杜绝循环依赖。
- **不变量**：单一真相源、ctx 一条到底（取消 + trace）、事件单向上抛、工具池运行期只读、function calling 配对完整、fail-closed。

> 详细的架构设计、模块接口与关键机制请参阅 [DEV_SPEC.md](spec/DEV_SPEC.md)。

---

## 配置说明

| 环境变量 | 含义 | 默认 |
| --- | --- | --- |
| `DEEPSEEK_API_KEY` | DeepSeek API 密钥（必需） | — |
| `COGENT_LLM_BASE_URL` | OpenAI 兼容 BaseURL | `https://api.deepseek.com/v1` |
| `COGENT_MODEL` | 模型名 | `deepseek-chat` |
| `COGENT_OBSERVE_ENABLED` | 可观测总开关 | `false` |
| `COGENT_TRACE_EXPORTER` | trace 导出后端 | `file` |
| `COGENT_LOG_LEVEL` | 日志级别 | `info` |

密钥仅从环境变量读取，代码与配置文件中严禁硬编码；`.env` 已被 `.gitignore` 排除。

---

## 开发

```bash
go build ./...
go test -race ./...   # 单元 + 组件测试（含 goroutine 泄漏检测）
```

### 目录结构

```text
cogent/
├── cmd/cogent/                 # CLI 入口：run / resume / mcp
├── internal/
│   ├── types/                  # Message / ToolUseBlock / StreamEvent
│   ├── engine/                 # ReAct 主循环（执行内核）
│   ├── llm/                    # DeepSeek 兼容客户端 + SSE
│   ├── tool/                   # Tool 接口 + Pool + 内建工具
│   ├── orchestrate/            # 并发/串行分批调度
│   ├── permission/             # 权限三态 + HITL
│   ├── sandbox/                # 命令沙箱 + 路径校验
│   ├── contextmgr/             # 窗口计算 + auto-compact
│   ├── memory/                 # MEMORY.md 加载
│   ├── session/                # JSONL 事件流 + resume
│   ├── mcp/                    # MCP client（stdio）
│   ├── agent/                  # SubAgent 派发
│   └── observe/                # OTel 封装：Tracer/Meter/Provider
├── eval/                       # 评测集与跑分脚本
├── data/                       # 运行期数据（.gitignore 排除）
├── spec/                       # DEV_SPEC / LOOP_SPEC / OPTIMIZE_SPEC
└── docs/                       # 技术教程
```

### 关键依赖

| 用途 | 选型 |
| --- | --- |
| CLI | `github.com/spf13/cobra` |
| LLM 客户端 | `github.com/sashabaranov/go-openai` |
| 并发 | `golang.org/x/sync/errgroup` |
| MCP | 自实现最小 stdio 客户端（不引入第三方 SDK） |
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
