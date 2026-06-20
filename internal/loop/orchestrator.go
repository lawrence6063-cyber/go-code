package loop

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/types"
	"github.com/alaindong/cogent/internal/verify"
)

// errEmptyIntent 表示目标循环缺少自然语言意图。
var errEmptyIntent = errors.New("empty goal intent")

// orchestrator 是 Orchestrator 的默认实现：外层目标循环，单次执行委托给内层 engine。
type orchestrator struct {
	engine engineRunner
	tracer observe.Tracer
	meter  observe.Meter
	cost   CostMeter
}

// engineRunner 是 orchestrator 对内层执行体的最小依赖（engine.Engine 满足之），
// 仅取其 Run 能力，便于测试注入替身且不引入更宽的耦合。
type engineRunner interface {
	Run(ctx context.Context, task string) (<-chan types.StreamEvent, error)
}

// RunGoal 见 Orchestrator 接口说明：校验意图后启动后台目标循环，返回只读事件流。
func (o *orchestrator) RunGoal(ctx context.Context, goal Goal) (<-chan LoopEvent, error) {
	if strings.TrimSpace(goal.Intent) == "" {
		return nil, errEmptyIntent
	}
	out := make(chan LoopEvent, 16)
	go func() {
		defer close(out)
		o.runGoal(ctx, goal, out)
	}()
	return out, nil
}

// runGoal 开 loop.run span、执行外层循环、记录指标并上抛终局事件。
// 终局事件用入参 ctx（非墙钟派生 ctx）门控：墙钟到顶仍能投递结局，仅在上游取消时跳过。
func (o *orchestrator) runGoal(ctx context.Context, goal Goal, out chan<- LoopEvent) {
	budget := withDefaults(goal.Budget)
	rctx, end := o.tracer.Start(ctx, "loop.run",
		observe.Attr{Key: "loop.max_iterations", Value: budget.MaxIterations})
	result := o.loop(rctx, goal, budget, time.Now(), out)
	end(nil)
	o.recordMetrics(result)
	res := result
	o.emit(ctx, out, LoopEvent{Type: LoopFinished, Iteration: res.Iterations, Result: &res})
}

// loop 是外层循环主体：每轮 engine.Run → 独立判定 → 达标即停 / 未达标带反馈续跑，
// 直至达标或撞轮数 / 成本 / 墙钟护栏。墙钟经派生 ctx 超时，取消贯穿内外层。
func (o *orchestrator) loop(
	ctx context.Context, goal Goal, budget Budget, start time.Time, out chan<- LoopEvent,
) LoopResult {
	deadline, cancel := budgetContext(ctx, budget)
	defer cancel()

	task := goal.Intent
	var last verify.Report
	for iter := 0; iter < budget.MaxIterations; iter++ {
		if deadline.Err() != nil {
			return o.result(stopOutcome(ctx), iter, last, start)
		}
		o.emit(deadline, out, LoopEvent{Type: LoopIterationStart, Iteration: iter})
		report, stopped := o.iterate(deadline, iter, task, goal, out)
		if stopped {
			return o.result(stopOutcome(ctx), iter+1, last, start)
		}
		last = report
		if report.Passed {
			return o.result(OutcomeAchieved, iter+1, last, start)
		}
		if o.overCost(budget) {
			return o.result(OutcomeBudgetSpent, iter+1, last, start)
		}
		task = feedbackPrompt(goal.Intent, report)
	}
	return o.result(OutcomeBudgetSpent, budget.MaxIterations, last, start)
}

// iterate 执行单轮：开 loop.iteration span，跑内层执行体并透传事件，随后独立判定。
// 返回本轮判定报告；stopped=true 表示 ctx（墙钟 / 取消）已结束，应终止外层循环。
func (o *orchestrator) iterate(
	ctx context.Context, iter int, task string, goal Goal, out chan<- LoopEvent,
) (report verify.Report, stopped bool) {
	ictx, end := o.tracer.Start(ctx, "loop.iteration", observe.Attr{Key: "iter.index", Value: iter})
	defer end(nil)
	o.runInner(ictx, task, out)
	if ctx.Err() != nil {
		return verify.Report{}, true
	}
	return o.verify(ictx, goal, out), false
}

// runInner 调内层 engine 跑一轮任务，把其 StreamEvent 经 LoopInner 透传；
// ctx 取消或内层 channel 关闭即返回。engine 自身在 ctx 取消时收尾并 close，无泄漏。
func (o *orchestrator) runInner(ctx context.Context, task string, out chan<- LoopEvent) {
	events, err := o.engine.Run(ctx, task)
	if err != nil {
		ev := types.StreamEvent{Type: types.EventError, Err: err}
		o.emit(ctx, out, LoopEvent{Type: LoopInner, Inner: &ev})
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if !o.emitInner(ctx, out, ev) {
				return
			}
		}
	}
}

// verify 调用独立判定器并上抛 LoopVerify；Verifier 为 nil 或判定异常一律 fail-closed=未通过。
func (o *orchestrator) verify(ctx context.Context, goal Goal, out chan<- LoopEvent) verify.Report {
	if goal.Verifier == nil {
		report := verify.Report{Summary: "no verifier configured (fail-closed: never achieved)"}
		o.emitVerify(ctx, out, report)
		return report
	}
	vctx, end := o.tracer.Start(ctx, "goal.verify")
	report, err := goal.Verifier.Verify(vctx, goal.WorkRoot, goal.Intent)
	end(err)
	if err != nil {
		slog.Warn("verifier error (fail-closed as not passed)", "err", err)
		report.Passed = false
	}
	o.meter.Count("cogent.verify.passed", boolToInt(report.Passed),
		observe.Attr{Key: "verify.passed", Value: report.Passed})
	o.emitVerify(ctx, out, report)
	return report
}

// overCost 报告是否撞成本护栏；CostMeter 缺省或未设上限时恒为 false。
func (o *orchestrator) overCost(budget Budget) bool {
	if o.cost == nil || budget.MaxCostUSD <= 0 {
		return false
	}
	return o.cost.SpentUSD() >= budget.MaxCostUSD
}

// result 组装一次循环结局，附带成本与耗时归因。
func (o *orchestrator) result(outcome Outcome, iters int, last verify.Report, start time.Time) LoopResult {
	r := LoopResult{Outcome: outcome, Iterations: iters, LastReport: last, Elapsed: time.Since(start)}
	if o.cost != nil {
		r.SpentUSD = o.cost.SpentUSD()
	}
	return r
}

// recordMetrics 记录目标循环的健康度指标（轮数分布 / 结局计数 / 成本）。
func (o *orchestrator) recordMetrics(r LoopResult) {
	o.meter.Record("cogent.loop.iterations", float64(r.Iterations))
	o.meter.Count("cogent.loop.outcome", 1, observe.Attr{Key: "loop.outcome", Value: r.Outcome.String()})
	if r.SpentUSD > 0 {
		o.meter.Record("cogent.loop.cost_usd", r.SpentUSD)
	}
}

// emit 在尊重 ctx 取消的前提下把外层事件送入 channel；返回 false 表示已取消。
func (o *orchestrator) emit(ctx context.Context, out chan<- LoopEvent, ev LoopEvent) bool {
	select {
	case <-ctx.Done():
		return false
	case out <- ev:
		return true
	}
}

// emitInner 透传一条内层事件（值拷贝，避免共享 range 变量地址）。
func (o *orchestrator) emitInner(ctx context.Context, out chan<- LoopEvent, ev types.StreamEvent) bool {
	inner := ev
	return o.emit(ctx, out, LoopEvent{Type: LoopInner, Inner: &inner})
}

// emitVerify 上抛一条判定事件（值拷贝）。
func (o *orchestrator) emitVerify(ctx context.Context, out chan<- LoopEvent, report verify.Report) bool {
	r := report
	return o.emit(ctx, out, LoopEvent{Type: LoopVerify, Report: &r})
}

// feedbackPrompt 把未达标的判定报告拼为下一轮 user task：作为新任务注入 engine.Run，
// 复用同一 Engine 实例的多轮历史累积，不触碰其内部 msgs（守 function calling 配对完整）。
func feedbackPrompt(intent string, r verify.Report) string {
	var sb strings.Builder
	sb.WriteString("The goal is not yet achieved. Keep working until verification passes.\n\n")
	sb.WriteString("Original goal: ")
	sb.WriteString(intent)
	sb.WriteString("\n\nVerification feedback (not passed):\n")
	sb.WriteString(r.Summary)
	if strings.TrimSpace(r.Detail) != "" {
		sb.WriteString("\n\nDetails:\n")
		sb.WriteString(r.Detail)
	}
	sb.WriteString("\n\nPlease fix the remaining issues and try again.")
	return sb.String()
}

// withDefaults 用保守默认补全零值 Budget：全零回退 DefaultBudget；缺轮数上限时单独补全，
// 确保任何配置都有一个有限的轮数护栏（预算先行不变量）。
func withDefaults(b Budget) Budget {
	if b.MaxIterations <= 0 && b.MaxCostUSD <= 0 && b.MaxWallClock <= 0 {
		return DefaultBudget()
	}
	if b.MaxIterations <= 0 {
		b.MaxIterations = DefaultBudget().MaxIterations
	}
	return b
}

// budgetContext 把墙钟上限转为派生 ctx 超时；未设墙钟时仅派生可取消 ctx。
func budgetContext(ctx context.Context, b Budget) (context.Context, context.CancelFunc) {
	if b.MaxWallClock > 0 {
		return context.WithTimeout(ctx, b.MaxWallClock)
	}
	return context.WithCancel(ctx)
}

// stopOutcome 区分「上游取消」与「墙钟到顶」：parent 已取消=Canceled，否则=BudgetSpent。
func stopOutcome(parent context.Context) Outcome {
	if parent.Err() != nil {
		return OutcomeCanceled
	}
	return OutcomeBudgetSpent
}

// boolToInt 把布尔映射为指标计数（true=1, false=0）。
func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
