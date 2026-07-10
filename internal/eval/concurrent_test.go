package eval

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/alaindong/cogent/internal/eval/adapter"
	"github.com/alaindong/cogent/internal/loop"
)

// TestMain 用 goleak 断言并发运行器不泄漏 goroutine（EVAL_SPEC §6.9）。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// countingExecutor 记录被调用次数，返回预置达标结局；用于并发/取消测试。
type countingExecutor struct {
	calls int64
	delay time.Duration
}

func (e *countingExecutor) Run(ctx context.Context, _ adapter.Case, _ string) (loop.LoopResult, error) {
	atomic.AddInt64(&e.calls, 1)
	if e.delay > 0 {
		select {
		case <-ctx.Done():
			return loop.LoopResult{Outcome: loop.OutcomeCanceled}, nil
		case <-time.After(e.delay):
		}
	}
	return loop.LoopResult{Outcome: loop.OutcomeAchieved, Iterations: 1}, nil
}

func manyCases(n int) []adapter.Case {
	cases := make([]adapter.Case, n)
	for i := range cases {
		cases[i] = adapter.Case{
			ID:              "native/c" + string(rune('a'+i)),
			ExpectedOutcome: "achieved",
			Meta:            adapter.Meta{Difficulty: "easy", Source: "native"},
		}
	}
	return cases
}

func TestConcurrentRunnerAllRun(t *testing.T) {
	cases := manyCases(8)
	ex := &countingExecutor{}
	rep, err := NewRunner().Run(context.Background(), cases, RunOptions{Executor: ex, Concurrency: 4})
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt64(&ex.calls) != 8 {
		t.Fatalf("executor calls=%d, want 8", ex.calls)
	}
	if rep.Total != 8 || rep.Metrics.Passed != 8 {
		t.Fatalf("report=%+v, want 8/8", rep.Metrics)
	}
	// 结果按 ID 排序，保证报告确定性。
	for i := 1; i < len(rep.Cases); i++ {
		if rep.Cases[i-1].ID > rep.Cases[i].ID {
			t.Fatalf("cases not sorted by ID: %s > %s", rep.Cases[i-1].ID, rep.Cases[i].ID)
		}
	}
}

func TestConcurrentRunnerCancelSafe(t *testing.T) {
	cases := manyCases(8)
	ex := &countingExecutor{delay: 50 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消：应停止派发新样本、无泄漏地收尾
	rep, err := NewRunner().Run(ctx, cases, RunOptions{Executor: ex, Concurrency: 4})
	if err == nil {
		t.Fatal("expected ctx.Err() on canceled run")
	}
	// 取消后完成的样本数应 <= 总数（不苛求精确值，只验证安全收尾与无泄漏）。
	if rep.Total > len(cases) {
		t.Fatalf("completed=%d exceeds total=%d", rep.Total, len(cases))
	}
}
