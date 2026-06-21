package loop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/progress"
)

// Daemon 把 Trigger 与 Orchestrator 接起来：每次触发跑一次 RunGoal，把结局写入
// progress.md 看板（§4.4），并尊重全局预算与 ctx 取消。串行消费触发信号（一次只跑一个
// RunGoal）以防并发膨胀；预算护栏仍是兜底（§5.1）。
type Daemon struct {
	Trigger Trigger        // 触发源（cron / fswatch / ...）
	Orch    Orchestrator   // 目标循环编排器
	Board   progress.Board // 跨 run 待办看板；nil 时不记录进度
	Tracer  observe.Tracer // loop.daemon span；nil 时不埋点
}

// Run 启动守护循环：阻塞直到 ctx 取消或触发源关闭；每次触发执行一轮目标循环并记录进度。
// goalOf 把一次触发信号映射为具体 Goal（由调用方按 goal-file / 触发载荷构造）。
func (d *Daemon) Run(ctx context.Context, goalOf func(TriggerSignal) Goal) error {
	if d.Trigger == nil || d.Orch == nil {
		return errors.New("daemon requires a trigger and an orchestrator")
	}
	if goalOf == nil {
		return errors.New("daemon requires a goal factory")
	}
	if d.Tracer != nil {
		var end observe.EndFunc
		ctx, end = d.Tracer.Start(ctx, "loop.daemon")
		defer end(nil)
	}
	signals, err := d.Trigger.Fire(ctx)
	if err != nil {
		return fmt.Errorf("start trigger: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case sig, ok := <-signals:
			if !ok {
				return ctx.Err()
			}
			d.runOnce(ctx, goalOf(sig))
		}
	}
}

// runOnce 执行一轮目标循环并把结局 Upsert 进看板；单轮失败仅告警，不中断守护进程。
func (d *Daemon) runOnce(ctx context.Context, goal Goal) {
	events, err := d.Orch.RunGoal(ctx, goal)
	if err != nil {
		slog.Warn("daemon: run goal", "err", err)
		return
	}
	result := drainLoop(ctx, events)
	d.record(ctx, goal, result)
}

// record 把一轮结局写入 progress 看板（按目标意图派生稳定 ID）；无看板则跳过。
func (d *Daemon) record(ctx context.Context, goal Goal, result LoopResult) {
	if d.Board == nil {
		return
	}
	item := progress.Item{
		ID:     goalID(goal.Intent),
		Title:  truncateTitle(goal.Intent),
		Status: statusFor(result.Outcome),
		Note:   fmt.Sprintf("%s after %d iter(s)", result.Outcome.String(), result.Iterations),
	}
	if err := d.Board.Upsert(ctx, goal.WorkRoot, item); err != nil {
		slog.Warn("daemon: upsert progress", "err", err)
	}
}

// drainLoop 排空一轮目标循环的事件流并返回其终局结果（无终局事件时返回零值）。
func drainLoop(ctx context.Context, events <-chan LoopEvent) LoopResult {
	var result LoopResult
	for {
		select {
		case <-ctx.Done():
			return result
		case ev, ok := <-events:
			if !ok {
				return result
			}
			if ev.Type == LoopFinished && ev.Result != nil {
				result = *ev.Result
			}
		}
	}
}

// statusFor 把目标循环结局映射为看板状态。
func statusFor(o Outcome) progress.Status {
	switch o {
	case OutcomeAchieved:
		return progress.StatusDone
	case OutcomeCanceled:
		return progress.StatusDoing
	default: // BudgetSpent / Fatal：需人介入
		return progress.StatusBlocked
	}
}

// goalID 由目标意图派生稳定的看板项 ID（小写、非字母数字归一为连字符、限长）。
func goalID(intent string) string {
	var sb strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(intent)) {
		if isIDChar(r) {
			sb.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			sb.WriteByte('-')
			prevDash = true
		}
	}
	id := strings.Trim(sb.String(), "-")
	if id == "" {
		return "goal"
	}
	return truncateRunes(id, 48)
}

// isIDChar 报告字符是否可直接用于 ID（小写字母 / 数字）。
func isIDChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

// truncateTitle 把意图截断为看板标题（限长，避免单元格过宽）。
func truncateTitle(intent string) string {
	return truncateRunes(strings.TrimSpace(intent), 80)
}

// truncateRunes 在 rune 边界把字符串截断到至多 max 个字符。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
