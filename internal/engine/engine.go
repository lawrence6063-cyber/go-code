// Package engine 实现 cogent 的无 UI 执行内核：承载单次任务的 ReAct 主循环。
// CLI 与 Headless 共用同一内核（单一真相源）；UI 仅消费 <-chan StreamEvent。
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/alaindong/cogent/internal/contextmgr"
	"github.com/alaindong/cogent/internal/llm"
	"github.com/alaindong/cogent/internal/memory"
	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/session"
	"github.com/alaindong/cogent/internal/tool"
	"github.com/alaindong/cogent/internal/types"
)

// ErrMaxStepsExceeded 表示 ReAct 主循环触达最大轮数护栏。
var ErrMaxStepsExceeded = errors.New("max react steps exceeded")

// ErrNothingToUndo 表示没有可撤销的轮次（已回退到底或刚启动）。
var ErrNothingToUndo = errors.New("nothing to undo")

// 内核默认参数。
const (
	DefaultMaxSteps       = 50                // ReAct 最大轮数，防失控空转（导出供 cmd 层引用）
	defaultModel          = "deepseek-chat"   // 默认模型名
	defaultLLMCallTimeout = 120 * time.Second // 单次 LLM 调用的独立超时上限（防首 token 长挂起，OPTIMIZE_SPEC R4）
)

// systemPrompt 见 prompt.go。

// Engine 是无 UI 的执行引擎，承载单次任务的 ReAct 主循环；CLI 与 Headless 共用。
type Engine interface {
	// Run 执行一次新任务，返回只读事件 channel；ctx 取消即中断并安全收尾。
	// 同一 Engine 实例的连续 Run 会累积消息历史，构成多轮对话（单一真相源）。
	// 约定：channel 关闭即代表本轮完成（含历史更新），调用方须消费至 close 再发起下一次 Run。
	Run(ctx context.Context, task string) (<-chan types.StreamEvent, error)
	// Resume 加载已有会话并从中断处继续（Phase 5 实现）。
	Resume(ctx context.Context, sessionID string) (<-chan types.StreamEvent, error)
	// Undo 撤销最近一轮对话：回退消息历史并恢复工作区快照（若有）。
	// 无可撤销轮次时返回 ErrNothingToUndo。
	Undo(ctx context.Context) (*UndoResult, error)
}

// UndoResult 描述一次 Undo 操作的结果摘要。
type UndoResult struct {
	Summary        string // 被撤销轮次的摘要（user 消息前 50 字符）
	HasFileChanges bool   // 是否恢复了工作区文件改动
	RemovedCount   int    // 被移除的消息数量
}

// Snapshotter 抽象工作区快照的创建与恢复，用于 Undo 时精确回退文件改动。
type Snapshotter interface {
	// Take 记录当前工作区状态快照，返回快照标识（如 git stash SHA）；工作区无改动时返回空字符串。
	Take(ctx context.Context) (string, error)
	// Restore 恢复到指定快照标识的工作区状态。id 为空时执行 git checkout -- . && git clean -fd。
	Restore(ctx context.Context, id string) error
	// IsGitRepo 检测当前工作目录是否为 git 仓库。
	IsGitRepo() bool
}

// turnSnapshot 记录每轮对话开始前的引擎状态，用于 Undo 回退。
type turnSnapshot struct {
	msgIndex int    // 该轮开始前 e.msgs 的长度
	stashID  string // 该轮开始前的工作区快照标识（Snapshotter.Take 返回值）
	lastUUID string // 该轮开始前的 session 事件链尾 UUID
}

// Mode 是 Engine 的运行档位，决定自主程度与可用工具面。
type Mode int

// 运行模式枚举。
const (
	ModeAuto Mode = iota // 默认：自主执行，受 permission/sandbox 约束
	ModePlan             // 只读探索 + 产出执行计划，不写不执行，待人批准
	ModeAsk              // 只读问答，不调用任何写/执行类工具
)

// String 返回运行档位的可读名称（auto/plan/ask），未知值回退为 "auto"。
func (m Mode) String() string {
	switch m {
	case ModePlan:
		return "plan"
	case ModeAsk:
		return "ask"
	default:
		return "auto"
	}
}

// Deps 是构造 Engine 所需的依赖集合，便于测试注入替身。
type Deps struct {
	LLM          llm.Client          // LLM 提供方
	Tools        tool.Pool           // 启动期装配、运行期只读的工具池；为 nil 时退化为无工具纯对话
	Context      *contextmgr.Manager // 上下文窗口与自动压缩；为 nil 时不压缩
	Memory       memory.Loader       // 分层记忆加载（读）；为 nil 时不注入记忆
	MemoryWriter memory.Writer       // 分层记忆写入；为 nil 时不持久化运行时记忆（工具层使用）
	Session      session.Store       // 会话事件落盘与 resume；为 nil 时不持久化（纯对话/测试）
	SessionID    string              // 当前会话 ID；Session 非 nil 时用于定位 transcript
	Observe      observe.Provider    // 可观测（trace/指标）；可传 no-op 关闭
	Snapshotter  Snapshotter         // 工作区快照管理；为 nil 时 Undo 不恢复文件（仅回退消息）
	Mode         Mode                // 运行档位；默认 ModeAuto
	Model        string              // 模型名，用于窗口计算与请求
	WorkRoot     string              // 工作根目录，路径校验与 memory 加载基准
	MaxSteps     int                 // ReAct 最大轮数，防失控空转
	LLMTimeout   time.Duration       // 单次 LLM 调用超时上限；<=0 时用 defaultLLMCallTimeout
}

// New 构造一个 Engine 实例；deps 中除 Observe 外的核心依赖必填（Tools 可选）。
func New(deps Deps) (Engine, error) {
	if deps.LLM == nil {
		return nil, errors.New("nil llm client")
	}
	if deps.Observe == nil {
		return nil, errors.New("nil observe provider")
	}
	e := &engine{
		llm:         deps.LLM,
		tools:       deps.Tools,
		ctxmgr:      deps.Context,
		session:     deps.Session,
		snapshotter: deps.Snapshotter,
		sessionID:   deps.SessionID,
		tracer:      deps.Observe.Tracer(),
		meter:       deps.Observe.Meter(),
		mode:        deps.Mode,
		model:       deps.Model,
		workRoot:    deps.WorkRoot,
		maxSteps:    deps.MaxSteps,
		llmTimeout:  deps.LLMTimeout,
	}
	if e.maxSteps <= 0 {
		e.maxSteps = DefaultMaxSteps
	}
	if e.llmTimeout <= 0 {
		e.llmTimeout = defaultLLMCallTimeout
	}
	if e.model == "" {
		e.model = defaultModel
	}
	e.msgs = []types.Message{{Role: types.RoleSystem, Text: buildSystemPrompt(deps.Memory, e.workRoot)}}
	return e, nil
}

// buildSystemPrompt 把基础系统提示、工作根环境感知提示与项目记忆（若有）拼成最终系统提示。
// 记忆是可选增强，加载失败仅告警不影响内核启动。
func buildSystemPrompt(mem memory.Loader, workRoot string) string {
	base := systemPrompt
	if strings.TrimSpace(workRoot) != "" {
		base += fmt.Sprintf(workspaceHintTemplate, workRoot)
	}
	if mem == nil {
		return base
	}
	text, err := mem.Build(context.Background(), workRoot)
	if err != nil {
		slog.Warn("load memory", "err", err)
		return base
	}
	if strings.TrimSpace(text) == "" {
		return base
	}
	return base + "\n\n# Project memory (.cogent/MEMORY.md)\n" + text
}

// engine 是 Engine 的具体实现，持有跨轮的消息历史作为单一真相源。
type engine struct {
	llm           llm.Client
	tools         tool.Pool
	ctxmgr        *contextmgr.Manager
	session       session.Store
	snapshotter   Snapshotter
	tracer        observe.Tracer
	meter         observe.Meter
	mode          Mode
	model         string
	workRoot      string
	sessionID     string
	lastUUID      string // 最近落盘事件的 UUID，用于 append-only 事件链的 ParentUUID
	maxSteps      int
	llmTimeout    time.Duration // 单次 LLM 调用超时上限
	used          int           // 最近一次调用的上下文 token 估计，用于压缩判定
	msgs          []types.Message
	turnSnapshots []turnSnapshot // 每轮对话开始前的快照栈，支持连续 Undo
}

// Run 见 Engine 接口说明。
func (e *engine) Run(ctx context.Context, task string) (<-chan types.StreamEvent, error) {
	if strings.TrimSpace(task) == "" {
		return nil, errors.New("empty task")
	}
	out := make(chan types.StreamEvent, 16)
	go func() {
		defer close(out)
		// cogent.session 是本次执行的根 span，贯穿整轮 ReAct。
		ctx, sessionEnd := e.tracer.Start(ctx, "cogent.session",
			observe.Attr{Key: "llm.model", Value: e.model},
		)
		var (
			steps    int
			outcome  string // 零值 "" 表示未设定（panic 场景）
			finalErr error
		)
		defer func() {
			if outcome == "" {
				outcome = "error" // panic 降级为 error
			}
			attrs := []observe.Attr{
				{Key: "session.total_steps", Value: steps},
				{Key: "session.outcome", Value: outcome},
			}
			if e.sessionID != "" {
				attrs = append(attrs, observe.Attr{Key: "session.id", Value: e.sessionID})
			}
			sessionEnd(finalErr, attrs...)
		}()

		safeGo(e.onPanic(ctx, out), func() {
			e.takeTurnSnapshot(ctx)
			userMsg := types.Message{Role: types.RoleUser, Text: task}
			e.msgs = append(e.msgs, userMsg)
			e.record(ctx, userMsg)
			var stepErr error
			steps, stepErr = e.step(ctx, out)
			finalErr = stepErr
			if ctx.Err() != nil {
				outcome = "cancelled"
			} else if stepErr != nil {
				outcome = "error"
			} else {
				outcome = "done"
			}
		})
	}()
	return out, nil
}

// onPanic 返回一个把 goroutine panic 降级为 EventError 的回调：记录脱敏后的告警并上抛错误，
// 使内核单点 panic 不击穿进程；恢复后本轮任务以失败收尾，不吞掉继续。
func (e *engine) onPanic(ctx context.Context, out chan<- types.StreamEvent) func(v any) {
	return func(v any) {
		err := fmt.Errorf("engine panic recovered: %v", v)
		slog.Error("engine goroutine panic", "panic", v)
		emitEvent(ctx, out, types.StreamEvent{Type: types.EventError, Err: err})
	}
}

// Resume 加载已有会话事件，重建并修复 function calling 配对后，注入"继续"提示，
// 交回同一 ReAct 步进循环续跑（守单一真相源：不分叉主循环，仅 bootstrap 不同）。
func (e *engine) Resume(ctx context.Context, sessionID string) (<-chan types.StreamEvent, error) {
	if e.session == nil {
		return nil, errors.New("resume requires a configured session store")
	}
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("empty session id")
	}
	events, err := e.session.Load(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load session: %w", err)
	}
	e.sessionID = sessionID
	rebuilt, lastUUID := rebuildMessages(events)
	rebuilt = filterUnresolvedToolUses(rebuilt)
	e.msgs = append(e.msgs[:1:1], rebuilt...) // 保留内核重新注入的 system 提示，接上重建历史
	e.lastUUID = lastUUID

	out := make(chan types.StreamEvent, 16)
	go func() {
		defer close(out)
		// cogent.session 是本次 resume 执行的根 span，贯穿整轮 ReAct。
		ctx, sessionEnd := e.tracer.Start(ctx, "cogent.session",
			observe.Attr{Key: "llm.model", Value: e.model},
		)
		var (
			steps    int
			outcome  string
			finalErr error
		)
		defer func() {
			if outcome == "" {
				outcome = "error"
			}
			attrs := []observe.Attr{
				{Key: "session.total_steps", Value: steps},
				{Key: "session.outcome", Value: outcome},
			}
			if e.sessionID != "" {
				attrs = append(attrs, observe.Attr{Key: "session.id", Value: e.sessionID})
			}
			sessionEnd(finalErr, attrs...)
		}()

		safeGo(e.onPanic(ctx, out), func() {
			e.takeTurnSnapshot(ctx)
			cont := types.Message{Role: types.RoleUser, Text: continuePrompt}
			e.msgs = append(e.msgs, cont)
			e.record(ctx, cont)
			var stepErr error
			steps, stepErr = e.step(ctx, out)
			finalErr = stepErr
			if ctx.Err() != nil {
				outcome = "cancelled"
			} else if stepErr != nil {
				outcome = "error"
			} else {
				outcome = "done"
			}
		})
	}()
	return out, nil
}

// step 是 ReAct 主循环的核心步进（不负责追加初始消息，由 Run/Resume 各自 bootstrap 后调用）：
// 流式调 LLM → 文本上抛 → 无工具调用则结束；有则串行/并发执行工具、回流 tool_result 后进入下一轮，
// 直至触达 maxSteps。每一步产生的消息同步 record 为 append-only 事件。
// 返回本轮实际执行的步数与终端错误（正常结束为 nil，出错/超步/ctx 取消为对应 error）。
func (e *engine) step(ctx context.Context, out chan<- types.StreamEvent) (int, error) {
	stepsTaken := 0
	defer func() { e.meter.Record("cogent.react.steps", float64(stepsTaken)) }()
	for step := 0; step < e.maxSteps; step++ {
		if ctx.Err() != nil {
			return stepsTaken, ctx.Err()
		}
		stepsTaken = step + 1
		stepStart := time.Now()
		sctx, end := e.tracer.Start(ctx, "react.step", observe.Attr{Key: "step.index", Value: step})
		reply, toolUses, err := e.streamAssistant(sctx, out)
		e.appendAssistant(ctx, reply, toolUses)
		if err != nil {
			e.endStep(end, stepStart, len(toolUses), "error", err)
			emitEvent(ctx, out, types.StreamEvent{Type: types.EventError, Err: err})
			return stepsTaken, err
		}
		if len(toolUses) == 0 {
			e.endStep(end, stepStart, 0, "done", nil)
			emitEvent(ctx, out, types.StreamEvent{Type: types.EventDone})
			return stepsTaken, nil
		}
		results := e.executeTools(sctx, toolUses, out)
		e.endStep(end, stepStart, len(toolUses), "tools", nil)
		e.appendResults(ctx, results)
		e.maybeCompact(ctx, out)
	}
	emitEvent(ctx, out, types.StreamEvent{Type: types.EventError, Err: ErrMaxStepsExceeded})
	return stepsTaken, ErrMaxStepsExceeded
}

// endStep 结束一个 react.step span：记录单轮耗时指标并补 tool_use.count/step.outcome 属性（O3/O4）。
func (e *engine) endStep(end observe.EndFunc, start time.Time, toolCount int, outcome string, err error) {
	e.meter.Record("cogent.step.duration", float64(time.Since(start).Milliseconds()))
	end(err,
		observe.Attr{Key: "tool_use.count", Value: toolCount},
		observe.Attr{Key: "step.outcome", Value: outcome},
	)
}

// maybeCompact 在配置了上下文管理器且触达阈值时压缩历史，成功后上抛 EventCompacted。
// 压缩失败仅告警并保留原历史（绝不丢历史）；熔断后 ShouldCompact 恒 false 自然跳过。
func (e *engine) maybeCompact(ctx context.Context, out chan<- types.StreamEvent) {
	if e.ctxmgr == nil {
		return
	}
	used := e.used
	if used == 0 {
		used = contextmgr.EstimateTokens(e.msgs)
	}
	if !e.ctxmgr.ShouldCompact(used, e.model) {
		return
	}
	before := used
	e.meter.Count("cogent.compact.count", 1)
	cctx, end := e.tracer.Start(ctx, "ctx.compact", observe.Attr{Key: "compact.tokens_before", Value: before})
	compacted, err := e.ctxmgr.Compact(cctx, e.msgs, e.llm)
	if err != nil {
		// 压缩失败会触发熔断（后续 ShouldCompact 恒 false），计入 circuit_open 指标并标注 span。
		e.meter.Count("cogent.compact.circuit_open", 1)
		end(err, observe.Attr{Key: "circuit_open", Value: true})
		slog.Warn("context compact failed", "err", err)
		return
	}
	e.msgs = compacted
	e.used = contextmgr.EstimateTokens(e.msgs)
	end(nil,
		observe.Attr{Key: "compact.tokens_after", Value: e.used},
		observe.Attr{Key: "circuit_open", Value: false},
	)
	emitEvent(ctx, out, types.StreamEvent{Type: types.EventCompacted})
}

// appendAssistant 把本轮助手回复（含可能的工具调用）追加进消息历史并记录为事件；
// 工具调用随 assistant 消息一并落入，保证 function calling 配对完整。
func (e *engine) appendAssistant(ctx context.Context, reply string, toolUses []types.ToolUseBlock) {
	if reply == "" && len(toolUses) == 0 {
		return
	}
	msg := types.Message{Role: types.RoleAssistant, Text: reply, ToolCalls: toolUses}
	e.msgs = append(e.msgs, msg)
	e.record(ctx, msg)
}

// appendResults 追加本批工具结果到历史并逐条记录为 tool_result 事件（保请求序）。
func (e *engine) appendResults(ctx context.Context, results []types.Message) {
	e.msgs = append(e.msgs, results...)
	for _, r := range results {
		e.record(ctx, r)
	}
}

// streamAssistant 发起一次流式 LLM 调用：文本增量即时上抛，并累积模型发起的工具调用。
// 同时采集首 token 延迟（ttft）与 token 用量，作为 llm.stream span 属性与指标上抛（OPTIMIZE_SPEC O2/O3）。
func (e *engine) streamAssistant(
	ctx context.Context,
	out chan<- types.StreamEvent,
) (reply string, toolUses []types.ToolUseBlock, err error) {
	ctx, end := e.tracer.Start(ctx, "llm.stream", observe.Attr{Key: "llm.model", Value: e.model})
	var st streamStats
	defer func() { end(err, st.attrs()...) }()

	// 每次 LLM 调用施加独立超时上限，防"首 token 迟迟不来"长时间挂起；超时即本轮失败，由 maxSteps 兜底。
	// 取「单次超时」与「父 ctx 剩余时间（如外层墙钟）」的较小值，收紧墙钟到顶时的取消滞后（发现⑥）。
	ctx, cancel := context.WithTimeout(ctx, e.effectiveLLMTimeout(ctx))
	defer cancel()

	deltas, err := e.llm.Stream(ctx, llm.Request{Messages: e.msgs, Tools: e.toolSchemas(), Model: e.model})
	if err != nil {
		return "", nil, fmt.Errorf("llm stream: %w", err)
	}
	start := time.Now()
	var sb strings.Builder
	for {
		select {
		case <-ctx.Done():
			return sb.String(), toolUses, ctx.Err()
		case d, ok := <-deltas:
			if !ok {
				return sb.String(), toolUses, nil
			}
			if d.Err != nil {
				return sb.String(), toolUses, d.Err
			}
			st.observeFirstToken(e.meter, start)
			if d.ToolCall != nil {
				toolUses = append(toolUses, *d.ToolCall)
			}
			if d.FinishReason != "" {
				st.finishReason = d.FinishReason
			}
			if d.Text != "" {
				sb.WriteString(d.Text)
				if !emitEvent(ctx, out, types.StreamEvent{Type: types.EventText, Text: d.Text}) {
					return sb.String(), toolUses, ctx.Err()
				}
			}
			if d.Usage != nil {
				st.recordUsage(e, *d.Usage)
			}
		}
	}
}

// streamStats 累积单次 LLM 流的可观测数据：首 token 延迟、token 用量与结束原因。
type streamStats struct {
	gotFirst     bool
	ttftMs       int64
	prompt       int
	completion   int
	finishReason string
}

// effectiveLLMTimeout 取「单次 LLM 调用超时上限」与「父 ctx 剩余时间（如外层墙钟）」的较小值。
// 当外层墙钟将到顶而剩余时间小于单次超时时，按剩余时间收紧本次调用超时，使墙钟更接近硬上界
// （避免墙钟已到却仍等满一次慢调用，发现⑥）。
func (e *engine) effectiveLLMTimeout(ctx context.Context) time.Duration {
	d := e.llmTimeout
	if dl, ok := ctx.Deadline(); ok {
		if remaining := time.Until(dl); remaining > 0 && remaining < d {
			return remaining
		}
	}
	return d
}

// observeFirstToken 在首个增量到达时记录首 token 延迟（ttft）到指标，仅记一次。
func (s *streamStats) observeFirstToken(meter observe.Meter, start time.Time) {
	if s.gotFirst {
		return
	}
	s.gotFirst = true
	s.ttftMs = time.Since(start).Milliseconds()
	meter.Record("cogent.llm.ttft", float64(s.ttftMs))
}

// recordUsage 记录 token 用量到引擎窗口估计与 cogent.tokens 计数器。
// 按 input/output 分别计数并附 token.kind 与 llm.model 属性，便于成本计量做差异定价（OPTIMIZE_SPEC R5）；
// 总量仍为 prompt+completion，聚合语义不变。
func (s *streamStats) recordUsage(e *engine, u llm.Usage) {
	s.prompt = u.PromptTokens
	s.completion = u.CompletionTokens
	e.used = u.PromptTokens + u.CompletionTokens
	modelAttr := observe.Attr{Key: "llm.model", Value: e.model}
	e.meter.Count("cogent.tokens", int64(u.PromptTokens),
		observe.Attr{Key: "token.kind", Value: "input"}, modelAttr)
	e.meter.Count("cogent.tokens", int64(u.CompletionTokens),
		observe.Attr{Key: "token.kind", Value: "output"}, modelAttr)
	slog.Debug("llm usage", "prompt", u.PromptTokens, "completion", u.CompletionTokens)
}

// attrs 把流统计组装为 llm.stream span 的结束属性。
func (s streamStats) attrs() []observe.Attr {
	return []observe.Attr{
		{Key: "llm.prompt_tokens", Value: s.prompt},
		{Key: "llm.completion_tokens", Value: s.completion},
		{Key: "llm.ttft_ms", Value: int(s.ttftMs)},
		{Key: "llm.finish_reason", Value: s.finishReason},
	}
}

// toolSchemas 导出当前运行档位下可暴露给模型的工具 function calling 声明。
func (e *engine) toolSchemas() []llm.ToolSchema {
	tools := e.toolsForMode()
	if len(tools) == 0 {
		return nil
	}
	schemas := make([]llm.ToolSchema, 0, len(tools))
	for _, t := range tools {
		schemas = append(schemas, llm.ToolSchema{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.InputSchema(),
		})
	}
	return schemas
}

// toolsForMode 按运行档位过滤可用工具：Plan/Ask 仅暴露只读工具（fail-closed）。
func (e *engine) toolsForMode() []tool.Tool {
	if e.tools == nil {
		return nil
	}
	all := e.tools.All()
	if e.mode == ModeAuto {
		return all
	}
	readOnly := make([]tool.Tool, 0, len(all))
	for _, t := range all {
		if t.IsReadOnly(nil) {
			readOnly = append(readOnly, t)
		}
	}
	return readOnly
}

// emitEvent 在尊重 ctx 取消的前提下把事件送入 channel；返回 false 表示已取消。
func emitEvent(ctx context.Context, out chan<- types.StreamEvent, ev types.StreamEvent) bool {
	select {
	case <-ctx.Done():
		return false
	case out <- ev:
		return true
	}
}
