// Package engine 实现 cogent 的无 UI 执行内核：承载单次任务的 ReAct 主循环。
// CLI 与 Headless 共用同一内核（单一真相源）；UI 仅消费 <-chan StreamEvent。
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

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

// 内核默认参数。
const (
	defaultMaxSteps = 16              // ReAct 最大轮数，防失控空转
	defaultModel    = "deepseek-chat" // 默认模型名
)

// systemPrompt 是注入到上下文最前面的系统提示。
const systemPrompt = "You are cogent, an autonomous coding agent runtime written in Go. " +
	"You operate inside a real code repository and can call the provided tools to read, search, " +
	"and modify files and run commands to accomplish the user's task. " +
	"Prefer acting via tools over guessing; inspect files before editing them. " +
	"Use relative paths within the workspace. When the task is complete, reply with a concise summary. " +
	"If no tools are available, respond in plain text."

// Engine 是无 UI 的执行引擎，承载单次任务的 ReAct 主循环；CLI 与 Headless 共用。
type Engine interface {
	// Run 执行一次新任务，返回只读事件 channel；ctx 取消即中断并安全收尾。
	// 同一 Engine 实例的连续 Run 会累积消息历史，构成多轮对话（单一真相源）。
	// 约定：channel 关闭即代表本轮完成（含历史更新），调用方须消费至 close 再发起下一次 Run。
	Run(ctx context.Context, task string) (<-chan types.StreamEvent, error)
	// Resume 加载已有会话并从中断处继续（Phase 5 实现）。
	Resume(ctx context.Context, sessionID string) (<-chan types.StreamEvent, error)
}

// Mode 是 Engine 的运行档位，决定自主程度与可用工具面。
type Mode int

// 运行模式枚举。
const (
	ModeAuto Mode = iota // 默认：自主执行，受 permission/sandbox 约束
	ModePlan             // 只读探索 + 产出执行计划，不写不执行，待人批准
	ModeAsk              // 只读问答，不调用任何写/执行类工具
)

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
	Mode         Mode                // 运行档位；默认 ModeAuto
	Model        string              // 模型名，用于窗口计算与请求
	WorkRoot     string              // 工作根目录，路径校验与 memory 加载基准
	MaxSteps     int                 // ReAct 最大轮数，防失控空转
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
		llm:       deps.LLM,
		tools:     deps.Tools,
		ctxmgr:    deps.Context,
		session:   deps.Session,
		sessionID: deps.SessionID,
		tracer:    deps.Observe.Tracer(),
		mode:      deps.Mode,
		model:     deps.Model,
		workRoot:  deps.WorkRoot,
		maxSteps:  deps.MaxSteps,
	}
	if e.maxSteps <= 0 {
		e.maxSteps = defaultMaxSteps
	}
	if e.model == "" {
		e.model = defaultModel
	}
	e.msgs = []types.Message{{Role: types.RoleSystem, Text: buildSystemPrompt(deps.Memory, e.workRoot)}}
	return e, nil
}

// buildSystemPrompt 把基础系统提示与项目记忆（若有）拼成最终系统提示。
// 记忆是可选增强，加载失败仅告警不影响内核启动。
func buildSystemPrompt(mem memory.Loader, workRoot string) string {
	if mem == nil {
		return systemPrompt
	}
	text, err := mem.Build(context.Background(), workRoot)
	if err != nil {
		slog.Warn("load memory", "err", err)
		return systemPrompt
	}
	if strings.TrimSpace(text) == "" {
		return systemPrompt
	}
	return systemPrompt + "\n\n# Project memory (.cogent/MEMORY.md)\n" + text
}

// engine 是 Engine 的具体实现，持有跨轮的消息历史作为单一真相源。
type engine struct {
	llm       llm.Client
	tools     tool.Pool
	ctxmgr    *contextmgr.Manager
	session   session.Store
	tracer    observe.Tracer
	mode      Mode
	model     string
	workRoot  string
	sessionID string
	lastUUID  string // 最近落盘事件的 UUID，用于 append-only 事件链的 ParentUUID
	maxSteps  int
	used      int // 最近一次调用的上下文 token 估计，用于压缩判定
	msgs      []types.Message
}

// Run 见 Engine 接口说明。
func (e *engine) Run(ctx context.Context, task string) (<-chan types.StreamEvent, error) {
	if strings.TrimSpace(task) == "" {
		return nil, errors.New("empty task")
	}
	out := make(chan types.StreamEvent, 16)
	go func() {
		defer close(out)
		userMsg := types.Message{Role: types.RoleUser, Text: task}
		e.msgs = append(e.msgs, userMsg)
		e.record(ctx, userMsg)
		e.step(ctx, out)
	}()
	return out, nil
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
		cont := types.Message{Role: types.RoleUser, Text: continuePrompt}
		e.msgs = append(e.msgs, cont)
		e.record(ctx, cont)
		e.step(ctx, out)
	}()
	return out, nil
}

// step 是 ReAct 主循环的核心步进（不负责追加初始消息，由 Run/Resume 各自 bootstrap 后调用）：
// 流式调 LLM → 文本上抛 → 无工具调用则结束；有则串行/并发执行工具、回流 tool_result 后进入下一轮，
// 直至触达 maxSteps。每一步产生的消息同步 record 为 append-only 事件。
func (e *engine) step(ctx context.Context, out chan<- types.StreamEvent) {
	for step := 0; step < e.maxSteps; step++ {
		if ctx.Err() != nil {
			return
		}
		sctx, end := e.tracer.Start(ctx, "react.step", observe.Attr{Key: "step.index", Value: step})
		reply, toolUses, err := e.streamAssistant(sctx, out)
		e.appendAssistant(ctx, reply, toolUses)
		if err != nil {
			end(err)
			emitEvent(ctx, out, types.StreamEvent{Type: types.EventError, Err: err})
			return
		}
		if len(toolUses) == 0 {
			end(nil)
			emitEvent(ctx, out, types.StreamEvent{Type: types.EventDone})
			return
		}
		results := e.executeTools(sctx, toolUses, out)
		end(nil)
		e.appendResults(ctx, results)
		e.maybeCompact(ctx, out)
	}
	emitEvent(ctx, out, types.StreamEvent{Type: types.EventError, Err: ErrMaxStepsExceeded})
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
	cctx, end := e.tracer.Start(ctx, "ctx.compact")
	compacted, err := e.ctxmgr.Compact(cctx, e.msgs, e.llm)
	end(err)
	if err != nil {
		slog.Warn("context compact failed", "err", err)
		return
	}
	e.msgs = compacted
	e.used = contextmgr.EstimateTokens(e.msgs)
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
func (e *engine) streamAssistant(
	ctx context.Context,
	out chan<- types.StreamEvent,
) (reply string, toolUses []types.ToolUseBlock, err error) {
	ctx, end := e.tracer.Start(ctx, "llm.stream", observe.Attr{Key: "llm.model", Value: e.model})
	defer func() { end(err) }()

	deltas, err := e.llm.Stream(ctx, llm.Request{Messages: e.msgs, Tools: e.toolSchemas(), Model: e.model})
	if err != nil {
		return "", nil, fmt.Errorf("llm stream: %w", err)
	}
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
			if d.ToolCall != nil {
				toolUses = append(toolUses, *d.ToolCall)
			}
			if d.Text != "" {
				sb.WriteString(d.Text)
				if !emitEvent(ctx, out, types.StreamEvent{Type: types.EventText, Text: d.Text}) {
					return sb.String(), toolUses, ctx.Err()
				}
			}
			if d.Usage != nil {
				e.used = d.Usage.PromptTokens + d.Usage.CompletionTokens
				slog.Debug("llm usage", "prompt", d.Usage.PromptTokens, "completion", d.Usage.CompletionTokens)
			}
		}
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
