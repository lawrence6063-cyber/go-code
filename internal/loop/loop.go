// Package loop 在 engine 之上实现目标驱动的外层自治循环（LOOP_SPEC §4.1）。
// 它消费 engine.Engine 的单次执行能力，把「执行一轮 → 独立判定 → 不达标带反馈续跑」
// 接成外层循环，直到达标或撞预算/取消。
//
// 依赖方向（守 DEV_SPEC §4.4「依赖只能向内」）：loop → engine 单向，engine 对 loop
// 零反向依赖；loop 另依赖叶子包 verify 与横切 observe。新增不变量：独立判定（执行体
// 无法篡改判定结果）、预算先行（无 Budget 即用保守默认，绝不无限循环）。
package loop

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alaindong/cogent/internal/engine"
	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/types"
	"github.com/alaindong/cogent/internal/verify"
)

// Goal 描述一次目标驱动循环的输入：自然语言意图 + 可验证的终止条件。
type Goal struct {
	Intent   string          // 给 Agent 的自然语言目标，如「修复 X 并保证全部测试通过」
	WorkRoot string          // 工作根目录，传给 Verifier
	Verifier verify.Verifier // 独立终止条件判定器；nil 时 fail-closed 视为永不达标
	Budget   Budget          // 预算护栏；零值时用保守默认（见 DefaultBudget）
}

// Budget 是自治循环的三重失控护栏（呼应 DEV_SPEC §7.8）。任一触顶即终止。
type Budget struct {
	MaxIterations int           // 外层循环最大轮数（每轮 = 一次 engine 执行 + 一次判定）
	MaxCostUSD    float64       // 累计 LLM 成本上限（由 CostMeter 喂入）；<=0 表示不限
	MaxWallClock  time.Duration // 端到端墙钟上限；<=0 表示不限
}

// DefaultBudget 返回保守默认护栏：宁可早停，不可失控烧钱（fail-closed）。
func DefaultBudget() Budget {
	return Budget{MaxIterations: 8, MaxCostUSD: 5, MaxWallClock: 15 * time.Minute}
}

// Outcome 是目标循环的结局枚举。
type Outcome int

// 目标循环结局枚举。
const (
	OutcomeAchieved    Outcome = iota // 判定器确认目标达成（唯一成功结局）
	OutcomeBudgetSpent                // 撞预算护栏（轮数 / 成本 / 墙钟）
	OutcomeCanceled                   // ctx 被上游取消（如 Ctrl-C）
	OutcomeFatal                      // 内层不可恢复错误
)

// String 返回结局的稳定字符串，用于指标标签与渲染。
func (o Outcome) String() string {
	switch o {
	case OutcomeAchieved:
		return "achieved"
	case OutcomeBudgetSpent:
		return "budget_spent"
	case OutcomeCanceled:
		return "canceled"
	case OutcomeFatal:
		return "fatal"
	default:
		return "unknown"
	}
}

// LoopResult 汇总一次目标循环的结局与归因，便于上层渲染与评估。
type LoopResult struct {
	Outcome    Outcome       // 结局
	Iterations int           // 实际执行轮数
	LastReport verify.Report // 最后一次判定报告（含未达标原因）
	SpentUSD   float64       // 累计成本（CostMeter 为 nil 时为 0）
	Elapsed    time.Duration // 端到端耗时
}

// LoopEventType 标识外层循环事件类型；内层 engine 的 StreamEvent 经 LoopInner 透传。
type LoopEventType int

// 外层循环事件类型枚举。
const (
	LoopIterationStart LoopEventType = iota // 新一轮开始（携带轮序）
	LoopInner                               // 透传内层 engine 的一条 StreamEvent
	LoopVerify                              // 一次判定完成（携带 verify.Report）
	LoopFinished                            // 循环结束（携带 LoopResult）
)

// LoopEvent 是外层循环向 UI 单向上抛的统一事件（事件单向上抛不变量）。
type LoopEvent struct {
	Type      LoopEventType
	Iteration int                // 当前轮序（从 0 起）
	Inner     *types.StreamEvent // Type=LoopInner 时透传内层事件
	Report    *verify.Report     // Type=LoopVerify 时携带判定报告
	Result    *LoopResult        // Type=LoopFinished 时携带最终结局
}

// CostMeter 读取自治循环至今的累计 LLM 成本（美元），用于驱动 MaxCostUSD 护栏。
// nil 时禁用成本护栏（仍由轮数 / 墙钟兜底）。
type CostMeter interface {
	SpentUSD() float64
}

// PipelineResult 是一轮「制造-审查」执行体的产出（消费侧最小视图）。
type PipelineResult struct {
	Summary  string // maker 改动摘要（透传给 UI）
	Approved bool   // reviewer 是否通过
	Feedback string // 未通过时的修改意见（拼入下一轮反馈）
}

// Pipeline 抽象「maker 实现 → reviewer 审查」的执行体。本接口在消费侧（loop）定义，
// agent 包的 MakerReviewer 经 cmd 层薄适配满足之——使 loop 无需 import agent（依赖更干净）。
type Pipeline interface {
	// Iterate 让实现者完成 task 并由独立审查者裁决，返回摘要与是否通过。
	Iterate(ctx context.Context, task string) (PipelineResult, error)
}

// Orchestrator 是目标驱动外层循环的编排器。
type Orchestrator interface {
	// RunGoal 执行目标循环：每轮跑一次 engine、独立判定、未达标则带反馈续跑，
	// 直至达标或撞预算 / 取消。返回只读事件流；ctx 取消即安全收尾。
	RunGoal(ctx context.Context, goal Goal) (<-chan LoopEvent, error)
}

// Deps 是构造 Orchestrator 的注入依赖（与 engine.Deps 风格一致，便于测试替身）。
type Deps struct {
	Engine   engine.Engine  // 单次任务执行体（Pipeline 为 nil 时必填）
	Pipeline Pipeline       // 可选：制造-审查双角色执行体；非 nil 时本轮走双角色（L2）
	Tracer   observe.Tracer // loop.* span 埋点；nil 时回退 no-op
	Meter    observe.Meter  // 预算 / 轮数指标；nil 时回退 no-op
	Cost     CostMeter      // 累计成本读取器；nil 时不计成本预算
}

// New 构造目标循环编排器；Engine 与 Pipeline 至少一个非空，Tracer/Meter 缺省回退 no-op。
func New(deps Deps) (Orchestrator, error) {
	if deps.Engine == nil && deps.Pipeline == nil {
		return nil, errors.New("loop requires an engine or a pipeline")
	}
	if deps.Tracer == nil || deps.Meter == nil {
		prov, err := observe.New(observe.Config{Enabled: false})
		if err != nil {
			return nil, fmt.Errorf("init noop observe: %w", err)
		}
		if deps.Tracer == nil {
			deps.Tracer = prov.Tracer()
		}
		if deps.Meter == nil {
			deps.Meter = prov.Meter()
		}
	}
	return &orchestrator{
		engine:   deps.Engine,
		pipeline: deps.Pipeline,
		tracer:   deps.Tracer,
		meter:    deps.Meter,
		cost:     deps.Cost,
	}, nil
}
