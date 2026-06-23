# cogent 数据流深度教学文档 — 生成 Prompt

> **用途**：将此 prompt 提供给 AI，生成一套完整的 HTML + SVG + Mermaid 交互式教学文档。
> **目标读者**：项目作者本人，用于内化"从 input 到 output 每一步数据是什么形状、经过了哪些函数、做了什么变换"的完整心智模型。
> **最终交付**：单个自包含 HTML 文件，可在浏览器直接打开。

---

## Prompt 正文

```markdown
你是一位资深 Go 架构师和技术写作者。你的任务是为 cogent（一个用 Go 编写的生产级自主编码 Agent 运行时）生成一份**从 Input 到 Output 的完整数据流深度教学文档**。

### 📌 项目概要

cogent 给定自然语言任务（如"修复这个 bug"），自主完成：探索代码 → 制定计划 → 调用工具读写文件/执行命令 → 跑测试验证 → 失败自我修正。全过程流式可见、可中断、可恢复。

### 📌 文档定位与目标受众

- **受众**：项目的开发者/作者本人，已经写过所有代码但需要一份系统化的"数据流地图"来巩固和口述
- **最终目的**：读完此文档后，能够从头到尾口述 "当用户在 CLI 输入一句话后，数据经过了哪些模块、哪些函数、以什么形状流转、每一步做了什么变换、最终输出是什么"
- **深度要求**：不是概览，是**逐函数级**的数据流追踪。能说清每个关键函数的输入类型→处理逻辑→输出类型

### 📌 项目架构（共 20 个 internal 包，按依赖层级）

#### 第 0 层：共享类型
- **types** — `Role`/`Message`/`ToolUseBlock`/`ToolResult`/`StreamEvent` 等全系统共享基础类型

#### 第 1 层：纯叶子包
- **secret** — 敏感信息脱敏规则
- **permission** — 权限三态裁决（allow/ask/deny）+ `Policy`/`Prompter` 接口
- **config** — 用户级配置加载
- **verify** — `Verifier` 接口 + `ScriptVerifier`（验收脚本执行）
- **progress** — 跨 run 待办看板
- **skills** — 技能包加载与注入
- **memory** — 分层记忆读写

#### 第 2 层：基础设施
- **sandbox** — 命令执行沙箱（危险命令拦截 + 受限环境 + 超时）
- **observe** — OpenTelemetry 可观测（Tracer/Meter/Provider）
- **session** — 会话持久化（append-only JSONL 事件流 + 密钥脱敏）
- **worktree** — git worktree 物理隔离

#### 第 3 层：LLM 与工具
- **llm** — LLM 客户端（DeepSeek/OpenAI 兼容，流式 SSE，`toolAcc` 累积并行 tool_calls）
- **tool** — 工具运行时协议（`Tool` 接口 + `Pool` + `Guard` 权限装饰器 + `Spawner` 接口）
- **contextmgr** — 上下文窗口管理与自动压缩

#### 第 4 层：MCP 协议
- **mcp** — 手写 MCP stdio 客户端（JSON-RPC 2.0，`mcp__<server>__<tool>` 命名）

#### 第 5 层：执行引擎
- **engine** — ReAct 主循环核心：`Run(task) → <-chan StreamEvent`
- **orchestrate** — 工具调度编排（并发/串行批次切分）

#### 第 6 层：Agent 与循环
- **agent** — SubAgent 派发 + MakerReviewer 双角色流水线
- **loop** — 目标驱动外层循环 + 守护进程 + 触发器

### 📌 核心数据类型定义（必须在文档中精确呈现）

```go
// types 包
type Role string  // "system" | "user" | "assistant" | "tool"

type Message struct {
    Role      Role
    Text      string
    ToolCalls []ToolUseBlock  // 仅 assistant 角色
    ToolUseID string          // 仅 tool 角色（与 ToolUseBlock.ID 配对）
    ToolName  string          // 仅 tool 角色
}

type ToolUseBlock struct {
    ID    string
    Name  string
    Input json.RawMessage
}

type ToolResult struct {
    Content string
    IsError bool
}

type StreamEvent struct {
    Type    EventType       // text/tool_start/tool_result/compacted/done/error
    Text    string          // 文本增量（EventText）
    ToolUse *ToolUseBlock   // 工具调用元信息（EventToolStart）
    Result  *ToolResult     // 执行结果（EventToolResult）
    Err     error           // 错误（EventError）
}

// llm 包
type Request struct {
    Messages    []types.Message
    Tools       []ToolSchema
    Model       string
    Temperature float64
    MaxTokens   int
}

type Delta struct {
    Text         string
    ToolCall     *types.ToolUseBlock
    Usage        *Usage
    FinishReason string
    Err          error
}

// tool 包
type Tool interface {
    Name() string
    Description() string
    InputSchema() json.RawMessage
    IsConcurrencySafe(input json.RawMessage) bool
    IsReadOnly(input json.RawMessage) bool
    CheckPermission(ctx, input) permission.Decision
    Call(ctx, input, ProgressSink) (types.ToolResult, error)
}
```

### 📌 文档必须覆盖的 5 条完整数据流路径

#### 路径 1：主交互流 — `cogent run "fix the bug"`
```
CLI stdin → Cobra 命令解析 → buildEngine() 装配依赖
  → Engine.Run(task) → 构建初始 Message[]
  → engine.step() 循环（最多 maxSteps=16 步）：
    → streamAssistant():
      → 构建 llm.Request{Messages, Tools, Model...}
      → llm.Client.Stream(Request) → <-chan Delta
        → toOpenAIRequest() 类型转换
        → pump() SSE 循环读帧
        → processFrame() 解析：文本增量 / toolAcc 累积 / usage
        → flush → 完整 ToolUseBlock
      → 消费 Delta channel:
        → Delta.Text → StreamEvent{EventText} 上抛
        → Delta.ToolCall → 累积到 Message.ToolCalls
        → 组装完整 assistant Message
    → 如果无 tool_calls → StreamEvent{EventDone} → 结束
    → executeTools():
      → orchestrate.PartitionBatches(toolCalls, pool)
        → 按 IsConcurrencySafe 切分并发批/串行批
      → 每个 ToolUseBlock:
        → pool.Get(name) → Tool
        → guard.Call(ctx, input, sink):
          → Policy.Evaluate → permission.Decision
          → Ask 时 → Prompter.Ask() → HITL 中断
          → Allow → inner.Call() → ToolResult
        → StreamEvent{EventToolResult} 上抛
    → 组装 tool role Messages (ToolUseID 配对)
    → session.Append(messages) → JSONL 落盘
    → contextmgr.MaybeCompact():
      → 检查 token 阈值
      → 触发时 LLM 生成摘要替换旧历史
      → StreamEvent{EventCompacted} 上抛
  → CLI 消费 <-chan StreamEvent → 终端渲染
```

#### 路径 2：SubAgent 派发 — `task` 工具
```
主 Engine step → ToolUseBlock{Name:"task"} 
  → task tool.Call()
  → agent.SubAgent.Run(subTask):
    → 创建独立子 Engine（独立 session、独立消息历史）
    → 子 Engine.Run(subTask) → <-chan StreamEvent
    → 消费子事件流 → 提取文本
    → 截断至 ≤8KB 摘要
  → 返回 ToolResult{Content: summary}
  → 回流主 Engine 消息历史
```

#### 路径 3：目标驱动循环 — `cogent goal "make tests pass" --verify ./verify.sh`
```
CLI → Orchestrator.RunGoal(Goal{Intent, Verifier, Budget}):
  → 迭代循环:
    → Engine.Run(intent + 反馈) → StreamEvent 流
    → Verifier.Verify(ctx):
      → ScriptVerifier: sandbox 执行 verify.sh
      → 退出码 0 → pass / 非0 → fail + stderr 反馈
    → pass → 返回成功
    → fail → 构建反馈消息 → 继续下一轮
    → 检查三重预算（轮数/成本/墙钟） → 超限退出
```

#### 路径 4：MakerReviewer 双角色流水线
```
Goal + Pipeline 模式:
  → worktree.Create() → 物理隔离分支
  → Maker（可写工具集）:
    → 独立 Engine.Run(task) → 修改代码
  → Reviewer（只读工具集）:
    → 独立 Engine.Run("审查 diff") → 生成裁决
    → 解析裁决：有明确 "APPROVED" → 通过（fail-closed）
  → ScriptVerifier 客观验证
  → 通过 → worktree.Merge() → 合回基线
  → 未通过 → worktree.Discard() → 清理
```

#### 路径 5：上下文压缩流
```
engine.step() → contextmgr.MaybeCompact():
  → 计算当前消息总 token 数
  → 超过阈值（如 80% 窗口）:
    → 找安全切点（避开 tool_use/tool_result 配对中间）
    → 取旧消息段 → 构建压缩 prompt
    → llm.Stream(压缩请求) → 生成摘要
    → 替换旧消息段为单条摘要 Message
    → StreamEvent{EventCompacted} 通知 UI
  → 连续失败 3 次 → 熔断，不再尝试压缩
```

### 📌 输出格式要求

生成**单个自包含 HTML 文件**（`cogent-dataflow-deep-dive.html`），要求：

#### 技术栈
- HTML5 + 内联 CSS + 内联 JavaScript
- Mermaid.js（通过 CDN 引入：`https://cdn.jsdelivr.net/npm/mermaid@10/dist/mermaid.min.js`）
- 内联 SVG 用于精细的数据类型变换图
- 无外部依赖，浏览器直接打开

#### 视觉风格
- 暗色主题（参考已有文档：`--bg: #0f172a; --card: #1e293b; --accent: #38bdf8; --accent2: #a78bfa; --accent3: #34d399`）
- 代码块使用 Go 语法高亮（手动着色 CSS class）
- 渐变标题，卡片式布局，圆角边框

#### 必须包含的图表类型

1. **全局数据流总览图**（Mermaid flowchart TB）
   - 展示从 CLI 输入到终端输出的完整模块链路
   - 每个节点标注模块名和核心函数
   - 边上标注数据类型（如 `[]Message` → `Request` → `<-chan Delta`）

2. **分层依赖架构图**（SVG）
   - 7 层依赖金字塔，每层列出包名
   - 箭头显示依赖方向（严格单向）
   - 用颜色区分不同层级

3. **Engine ReAct 循环详细时序图**（Mermaid sequence diagram）
   - 参与者：CLI, Engine, LLM, ToolPool, Guard, Sandbox, Session, ContextMgr
   - 标注每次调用的输入/输出数据类型
   - 展示循环、条件分支、HITL 中断

4. **数据类型变换链路图**（SVG）
   - 为路径 1 中的每个关键变换画一个"管道"：
     - `string` → `Message{Role:user}` → `llm.Request` → `openai.ChatCompletionRequest`
     - `SSE frame` → `Delta` → `Message{Role:assistant, ToolCalls}` → `ToolUseBlock`
     - `json.RawMessage` → `ToolResult` → `Message{Role:tool}` → 回流消息历史
   - 每个"管道段"标注执行此变换的函数名

5. **工具执行流详细图**（Mermaid flowchart LR）
   - 从 `ToolUseBlock` 到 `ToolResult` 的完整决策树
   - 包含 Guard/Permission/Policy/Prompter/HITL 各环节
   - 标注 allow/ask/deny 三条路径

6. **目标循环 + MakerReviewer 流程图**（Mermaid flowchart）
   - 外层 Goal 循环
   - 内层 Pipeline 分支（单 engine vs worktree 隔离）
   - Maker → Reviewer → Verify 决策链

7. **会话持久化与恢复数据流图**（Mermaid sequence diagram）
   - Session.Append 写入 JSONL
   - Resume 时 Rebuild 重放
   - 密钥脱敏时机

#### 文档结构要求

```
1. 封面 — 标题 + 学习目标 + 阅读指引
2. 前置知识 — 必须理解的核心类型（带代码定义 + 字段用途注释）
3. 全局鸟瞰 — 全局数据流总览图 + 分层依赖架构图
4. 路径 1 逐步拆解 — 主交互流（最详细，每个函数一个小节）
   4.1 CLI 入口 → 装配阶段
   4.2 Engine.Run() → 消息初始化
   4.3 streamAssistant() → LLM 调用
   4.4 LLM 流式处理 → Delta 泵送
   4.5 Delta 消费 → 文本/工具调用提取
   4.6 executeTools() → 工具编排
   4.7 Guard 权限检查 → HITL 中断
   4.8 工具实际执行 → 结果回流
   4.9 Session 持久化 → 上下文压缩检查
   4.10 循环判定 → 下一步/结束
5. 路径 2 — SubAgent 派发流
6. 路径 3 — 目标驱动循环流
7. 路径 4 — MakerReviewer 双角色流
8. 路径 5 — 上下文压缩流
9. 交叉切面专题
   9.1 权限模型全景（Policy → Guard → Prompter）
   9.2 可观测链路（OpenTelemetry span 嵌套）
   9.3 安全纵深（沙箱 → 脱敏 → 权限 → fail-closed）
   9.4 错误处理与重试（LLM 退避 + 熔断 + 工具 isError）
10. 口述练习 — 为每条路径提供一段"口述模板"（填空式）
11. 速查卡 — 所有模块的单句职责 + 核心函数签名
```

#### 交互增强
- 每个章节有**折叠/展开**控制（`<details><summary>`）
- 图表支持**点击高亮当前路径**
- 代码块支持**hover 显示类型说明 tooltip**
- 页面顶部有**目录导航**（锚点跳转）
- **"口述模式"按钮**：点击后隐藏所有代码细节，只留流程图和口述模板

#### 质量标准
- 所有模块名、函数名、类型名必须与实际代码一致（不能编造）
- 数据类型标注必须精确到字段级别
- 每个 Mermaid 图必须可渲染（注意语法正确性，避免特殊字符冲突）
- 所有 SVG 必须内联（不引用外部文件）
- 文件大小控制在合理范围（<500KB）
- 移动端自适应

#### 口述模板示例（第 10 节参考格式）

> **路径 1 口述模板**：
> "用户在终端输入 `cogent run 'fix the bug'`，Cobra 解析到 `run` 子命令，
> 调用 `buildEngine()` 装配 ___（需填：LLM 客户端、工具池、会话管理器、观测器）___。
> Engine.Run() 将任务文本包装为 `Message{Role: ___, Text: ___}`，
> 与 system prompt 一起组成初始 `[]Message`……"

### 📌 特别注意事项

1. **不要泛泛而谈**：每讲一个模块，必须具体到"调用了哪个函数、输入什么类型、输出什么类型、做了什么变换"
2. **保持叙事连贯**：按数据实际流动顺序讲解，不要跳跃
3. **突出数据形变**：在数据从一个类型变成另一个类型的地方，用视觉高亮强调（如不同颜色的类型标签）
4. **关注配对关系**：ToolUseBlock.ID 与 tool role Message.ToolUseID 的配对是理解的关键
5. **接口解耦点**：在 Spawner、Pipeline、engineRunner 等接口解耦的地方明确说明"这里用接口打断了包依赖"
6. **并发模型**：executeTools 中并发批/串行批的切分逻辑要讲清楚
7. **fail-closed 原则**：在权限、reviewer 裁决等处强调 fail-closed 的安全设计
```

---

## 使用说明

1. 将上面 **Prompt 正文** 部分（` ```markdown ` 包裹的内容）完整复制
2. 附上 `@cogent` 项目上下文
3. 发送给 AI，要求生成 `cogent-dataflow-deep-dive.html`
4. 生成的 HTML 文件放到 `cogent/docs/` 目录下即可浏览器打开

## 增强建议

- 如果一次生成的文档过长导致截断，可将 prompt 拆分为多次：
  - 第一次：生成路径 1 + 全局图（最核心）
  - 第二次：生成路径 2-5
  - 第三次：生成交叉切面 + 口述练习 + 速查卡
- 每次生成后可追问"请检查所有 Mermaid 语法是否正确"来修正渲染问题
