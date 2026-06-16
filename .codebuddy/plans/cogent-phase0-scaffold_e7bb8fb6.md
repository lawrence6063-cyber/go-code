---
name: cogent-phase0-scaffold
overview: 为 cogent 项目搭建 Phase 0 脚手架与地基：初始化 go module、目录骨架、配置文件，接入 cobra 子命令(run/resume/mcp 占位)与 slog，定义 internal/types 共享类型，实现 internal/observe 的 no-op Provider 骨架，使 `cogent run "hello"` 能启动、读 env、流式打印事件并优雅退出。
todos:
  - id: init-module
    content: 初始化 go module(github.com/alaindong/cogent, go 1.22)、引入 cobra、创建 cmd/cogent 与 internal 目录骨架
    status: completed
  - id: project-config
    content: 创建 .gitignore（排除 /data、.env、二进制、覆盖率等）与 .env.example（按 §8.4 列出全部 env 项）
    status: completed
    dependencies:
      - init-module
  - id: types-pkg
    content: 实现 internal/types：Role/Message/ToolUseBlock/ToolResult/EventType/StreamEvent，全部导出符号带 Go 规范注释
    status: completed
    dependencies:
      - init-module
  - id: observe-pkg
    content: 实现 internal/observe：Tracer/EndFunc/Attr/Meter/Provider 接口 + Config + New，提供零开销 no-op 实现
    status: completed
    dependencies:
      - init-module
  - id: cli-commands
    content: 实现 cmd/cogent：main.go(slog 接入+读 COGENT_LOG_LEVEL+signal.NotifyContext 优雅退出) 与 commands.go(run 流式打印占位事件、resume/mcp 占位)
    status: completed
    dependencies:
      - types-pkg
      - observe-pkg
  - id: verify-build
    content: 运行 go mod tidy、gofmt/goimports/go vet 校验，并验证 cogent run "hello" 启动/打印/Ctrl-C 退出闭环
    status: completed
    dependencies:
      - project-config
      - cli-commands
---

## 产品概述

为 `cogent`（用 Go 编写的自主编码 Agent 运行时）搭建 **Phase 0 脚手架与地基**，交付一个"跑得起来的空壳"：能启动、读取环境变量、流式打印事件、优雅退出，为后续 Phase 1+ 的 ReAct 内核、LLM 接入、工具层等奠定可扩展骨架。

## 核心功能

- **项目初始化**：go module（`github.com/alaindong/cogent`）、目录骨架、`.gitignore`、`.env.example`。
- **CLI 子命令骨架**：基于 cobra 的 `cogent` 根命令 + `run` / `resume` / `mcp` 子命令；`run` 接收任务字符串参数，`resume` / `mcp` 为占位实现。
- **结构化日志**：接入标准库 `slog`，日志级别从环境变量 `COGENT_LOG_LEVEL` 读取。
- **优雅退出**：捕获 `Ctrl-C`（SIGINT/SIGTERM），通过 `context` 取消实现安全收尾。
- **共享类型层**：`internal/types` 定义 `Message` / `ToolUseBlock` / `ToolResult` / `StreamEvent` / `Role` / `EventType` 等跨包共享类型。
- **可观测 no-op 骨架**：`internal/observe` 定义 `Provider` / `Tracer` / `Meter` 接口及 no-op 实现（零开销、调用点无需 if 分支），真实 OTel exporter 留待 Phase 8。

## 交付验收

- `go build ./...` 通过；`gofmt` / `goimports` / `go vet` 无差异/告警。
- `go run ./cmd/cogent run "hello"` 能启动、按 `COGENT_LOG_LEVEL` 配置日志、流式打印若干 `StreamEvent`（如文本事件 + 完成事件）、`Ctrl-C` 可优雅退出。

## 边界（严格守 Phase 0）

- 不实现 LLM 客户端、真实 ReAct 循环、工具/权限/沙箱/contextmgr/memory/session/mcp/agent 等业务逻辑。
- observe 仅 no-op，不接真实 exporter；不创建 Phase 1+ 才需要的空包目录。

## 技术栈

- **语言**：Go 1.22（环境已确认 go1.22.5 darwin/arm64）
- **module path**：`github.com/alaindong/cogent`
- **CLI 框架**：`github.com/spf13/cobra`（子命令分流，§13 依赖表选型）
- **日志**：标准库 `log/slog`（结构化日志，后续对齐 trace_id）
- **信号/取消**：标准库 `os/signal` + `signal.NotifyContext` + `context`
- **格式化/校验**：`gofmt` / `goimports`（已装）/ `go vet`

## 实现策略

采用 DEV_SPEC §13 的目录骨架，但**严格按 Phase 0 范围只落地三个真实包**：`cmd/cogent`、`internal/types`、`internal/observe`，避免创建空目录技术债。整体遵循 §4.4 依赖方向不变量——`types` 为最内层共享类型层、不依赖任何业务包；`observe` 为横切薄接口；`cmd/cogent` 负责装配（wiring）。

**关键技术决策**：

1. **`ToolResult` 收敛到 `types` 包**：§5.1 的 `StreamEvent.Result` 引用 §5.4 定义在 `tool` 包的 `ToolResult`。为避免 Phase 0 引入 `tool` 包依赖，且遵守"`types` 不依赖任何业务包"（§4.4），将 `ToolResult{Content string; IsError bool}` 定义在 `types` 包内，后续 `tool` 包引用 `types.ToolResult`。这是对 spec 的合理收敛。
2. **observe no-op 零开销设计**（§5.11/§6.8）：`Tracer.Start` 返回原始 `ctx` + 空 `EndFunc`；`Meter.Count/Record` 空实现；`Provider.Shutdown` 返回 nil。业务调用点恒定存在但无需 `if` 分支——为 Phase 1 埋点"随机制同步写入、不留技术债"做准备。
3. **CLI 占位事件流**：`run` 命令 Phase 0 不接 LLM，仅构造几个 `StreamEvent`（如 `EventText("hello")` + `EventDone`）经一条 channel 流式打印，演示"事件单向上抛"（§4.4 不变量）的骨架形态。
4. **优雅退出**：`main` 用 `signal.NotifyContext` 把 `Ctrl-C` 转为 ctx 取消，子命令监听 `ctx.Done()` 安全收尾（§4.3/§6.5）。

## Go 规范遵循

error 末位且必处理；`main` 启动失败 fail-fast（`os.Exit(1)` 前打印错误，不用 panic 作常规错误流）；导出符号必带注释；函数 < 80 行、嵌套 < 4 层、参数 ≤ 5；import 分组（标准库/第三方）；文件名小写下划线。

## 目录结构

```
cogent/
├── cmd/
│   └── cogent/
│       └── main.go          # [NEW] CLI 入口：构造 root cmd、signal.NotifyContext 捕获 Ctrl-C、装配 slog、调用 cobra Execute；启动失败 fail-fast
│       └── commands.go      # [NEW] cobra 子命令定义：root(cogent) + run/resume/mcp；run 解析 task 参数并流式打印占位 StreamEvent；resume/mcp 打印未实现占位
├── internal/
│   ├── types/
│   │   └── types.go         # [NEW] 共享类型：Role 及枚举、Message、ToolUseBlock、ToolResult、EventType 及枚举、StreamEvent；纯类型零业务依赖；导出符号带注释
│   └── observe/
│       └── observe.go       # [NEW] 接口：Tracer/EndFunc/Attr/Meter/Provider + Config struct + New(cfg)；no-op 实现（零开销、Start 返回原 ctx）
├── .gitignore               # [NEW] 排除 /data、.env、二进制 /cogent、*.test、覆盖率文件、编辑器/OS 文件
├── .env.example             # [NEW] 列出 §8.4 env：DEEPSEEK_API_KEY、COGENT_LLM_BASE_URL、COGENT_OBSERVE_ENABLED、COGENT_TRACE_EXPORTER、COGENT_TRACE_DIR、COGENT_OTLP_ENDPOINT、COGENT_TRACE_SAMPLE_RATIO、COGENT_LOG_LEVEL
├── go.mod                   # [NEW] module github.com/alaindong/cogent; go 1.22; require cobra
└── go.sum                   # [NEW] go mod tidy 生成
```

## 关键类型结构（types 包，遵循 §5.1）

```
// Role 表示一条消息的角色。
type Role string

// Message 是上下文中的一条消息，可携带文本、工具调用或工具结果。
type Message struct {
    Role      Role
    Text      string
    ToolCalls []ToolUseBlock
    ToolUseID string
    ToolName  string
}

// ToolResult 是工具执行结果（收敛至 types 以避免业务包依赖）。
type ToolResult struct {
    Content string
    IsError bool
}

// StreamEvent 是执行内核向上游流式输出的统一事件。
type StreamEvent struct {
    Type    EventType
    Text    string
    ToolUse *ToolUseBlock
    Result  *ToolResult
    Err     error
}
```