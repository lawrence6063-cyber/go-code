package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/alaindong/cogent/internal/loop"
	"github.com/alaindong/cogent/internal/types"
	"github.com/alaindong/cogent/internal/verify"
)

// RunLoopView 消费目标循环的外层事件流并渲染到 stdout；ctx 取消时安全收尾。
// 透传内层 engine 文本/工具事件（复用流式渲染的分区与折叠），单独呈现轮次、判定与终局。
func RunLoopView(ctx context.Context, events <-chan loop.LoopEvent) error {
	for {
		select {
		case <-ctx.Done():
			fmt.Println("\n[interrupted]")
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			if err := printLoopEvent(ev); err != nil {
				return err
			}
		}
	}
}

// printLoopEvent 渲染单个外层事件：透传内层文本/工具事件，单独呈现轮次、判定与终局。
// 内层错误不中断外层渲染（外层会带反馈续跑），仅打印到 stderr。
func printLoopEvent(ev loop.LoopEvent) error {
	switch ev.Type {
	case loop.LoopIterationStart:
		fmt.Printf("\n=== iteration %d ===\ncogent> ", ev.Iteration+1)
	case loop.LoopInner:
		return printInnerEvent(ev.Inner)
	case loop.LoopVerify:
		if ev.Report != nil {
			printVerifyReport(*ev.Report)
		}
	case loop.LoopFinished:
		if ev.Result != nil {
			printLoopResult(*ev.Result)
		}
	}
	return nil
}

// printInnerEvent 透传内层 engine 事件；错误事件降级为告警，不冒泡中断外层循环。
func printInnerEvent(inner *types.StreamEvent) error {
	if inner == nil {
		return nil
	}
	if inner.Type == types.EventError {
		if inner.Err != nil {
			fmt.Fprintln(os.Stderr, "\n[inner error]", inner.Err)
		}
		return nil
	}
	return printEvent(*inner)
}

// printVerifyReport 渲染一次独立判定的结论。
func printVerifyReport(r verify.Report) {
	status := "NOT PASSED"
	if r.Passed {
		status = "PASSED"
	}
	fmt.Printf("\n[verify] %s — %s\n", status, r.Summary)
}

// printLoopResult 渲染目标循环的终局归因。
func printLoopResult(r loop.LoopResult) {
	fmt.Printf("\n=== loop finished: %s after %d iteration(s) in %s ===\n",
		r.Outcome.String(), r.Iterations, r.Elapsed.Round(time.Millisecond))
	if r.Outcome != loop.OutcomeAchieved && strings.TrimSpace(r.LastReport.Summary) != "" {
		fmt.Printf("  last verification: %s\n", r.LastReport.Summary)
	}
}
