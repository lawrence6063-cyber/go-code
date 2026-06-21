// Package agent 实现 SubAgent 派发：以独立上下文的子 Engine 执行可隔离的探索子任务，
// 只把结果摘要回流主循环，从而避免大量中间消息污染主 Agent 的上下文（DEV_SPEC §3.9、§6.7）。
//
// 依赖方向（§4.4）：agent 仅依赖 engine（用于新建子 Engine）与 observe/types，
// 不依赖 tool 包；它返回的 *SubAgent 隐式满足 tool.Spawner 接口，由 cmd 注入 task 工具。
package agent

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/alaindong/cogent/internal/engine"
	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/types"
)

// 子 Agent 默认参数。
const (
	defaultMaxSummaryBytes  = 8 * 1024 // 摘要回流上限，防止子任务输出撑大主上下文
	defaultSubAgentMaxSteps = 8        // 子任务 ReAct 轮数上限，比主循环更紧以防失控
)

// SubAgent 是 SubAgent 派发器：每次 Spawn 用模板依赖新建一个隔离子 Engine 执行子任务。
// 它隐式满足 tool.Spawner（Spawn 方法），无需反向 import tool 包。
type SubAgent struct {
	deps            engine.Deps    // 子 Engine 模板依赖（含 LLM、受限只读工具池、observe 等）
	tracer          observe.Tracer // 派发埋点；用于 agent.spawn span
	maxSummaryBytes int            // 摘要截断上限
}

// New 用模板依赖构造一个 SubAgent 派发器。deps 应配置受限（只读）工具池与较小的 MaxSteps；
// 会话持久化由派发器强制关闭以保证上下文隔离。
func New(deps engine.Deps) *SubAgent {
	if deps.MaxSteps <= 0 {
		deps.MaxSteps = defaultSubAgentMaxSteps
	}
	sa := &SubAgent{deps: deps, maxSummaryBytes: defaultMaxSummaryBytes}
	if deps.Observe != nil {
		sa.tracer = deps.Observe.Tracer()
	}
	return sa
}

// Spawn 以独立消息历史与受限工具池执行子任务，消费其事件流累积为摘要回传。
// 上下文隔离：强制 Session=nil（不写主 transcript）、独立 Engine 实例（独立 msgs）。
// 子 Engine 与父任务共用同一 ctx，父取消即全链路收手；其 span 自动挂为 agent.spawn 的子节点。
func (s *SubAgent) Spawn(ctx context.Context, prompt string) (summary string, err error) {
	sub := s.deps
	sub.Session = nil // 子 Agent 不落盘，避免污染主任务会话
	sub.SessionID = ""
	eng, err := engine.New(sub)
	if err != nil {
		return "", fmt.Errorf("build sub-agent: %w", err)
	}

	ctx, end := s.tracer.Start(ctx, "agent.spawn")
	var summaryLen int
	defer func() { end(err, observe.Attr{Key: "summary.bytes", Value: summaryLen}) }()

	events, err := eng.Run(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("run sub-agent: %w", err)
	}
	summary = s.collectSummary(events)
	summaryLen = len(summary)
	return summary, nil
}

// collectSummary 排空子 Engine 的事件流并累积文本为摘要（截断到上限）。
// 完整 range 至 channel 关闭可保子 Engine goroutine 不泄漏（其在 ctx 取消时也会关闭 out）。
func (s *SubAgent) collectSummary(events <-chan types.StreamEvent) string {
	return collectText(events, s.maxSummaryBytes)
}

// truncateSummary 把摘要截断到不超过 maxBytes，并在 UTF-8 字符边界处切断避免乱码。
func truncateSummary(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n... [sub-agent summary truncated]"
}
