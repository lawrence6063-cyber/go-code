package loop

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alaindong/cogent/internal/progress"
)

// fakeTrigger 投递固定数量的触发信号后关闭 channel（驱动 Daemon 确定性运行）。
type fakeTrigger struct {
	n int
}

func (f fakeTrigger) Fire(ctx context.Context) (<-chan TriggerSignal, error) {
	out := make(chan TriggerSignal)
	go func() {
		defer close(out)
		for i := 0; i < f.n; i++ {
			select {
			case <-ctx.Done():
				return
			case out <- TriggerSignal{Source: "fake"}:
			}
		}
	}()
	return out, nil
}

// blockingTrigger 投递一个信号后阻塞直到 ctx 取消（用于测试优雅停）。
type blockingTrigger struct{}

func (blockingTrigger) Fire(ctx context.Context) (<-chan TriggerSignal, error) {
	out := make(chan TriggerSignal)
	go func() {
		defer close(out)
		select {
		case <-ctx.Done():
		case out <- TriggerSignal{Source: "block"}:
			<-ctx.Done()
		}
	}()
	return out, nil
}

// fakeOrch 是 Orchestrator 替身：记录 RunGoal 调用次数，返回一个只含终局事件的流。
type fakeOrch struct {
	mu     sync.Mutex
	calls  int
	result LoopResult
}

func (f *fakeOrch) RunGoal(_ context.Context, _ Goal) (<-chan LoopEvent, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	out := make(chan LoopEvent, 1)
	res := f.result
	out <- LoopEvent{Type: LoopFinished, Result: &res}
	close(out)
	return out, nil
}

func (f *fakeOrch) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestDaemon_RunsPerTriggerAndRecordsProgress(t *testing.T) {
	root := t.TempDir()
	fo := &fakeOrch{result: LoopResult{Outcome: OutcomeAchieved, Iterations: 2}}
	board := progress.NewBoard()
	d := &Daemon{Trigger: fakeTrigger{n: 3}, Orch: fo, Board: board}

	err := d.Run(context.Background(), func(TriggerSignal) Goal {
		return Goal{Intent: "auto fix failing tests", WorkRoot: root}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fo.count() != 3 {
		t.Errorf("RunGoal called %d times, want 3 (one per trigger)", fo.count())
	}
	items, err := board.Load(context.Background(), root)
	if err != nil {
		t.Fatalf("board.Load: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("board items = %d, want 1 (same goal upserted)", len(items))
	}
	if items[0].Status != progress.StatusDone {
		t.Errorf("status = %v, want done (achieved)", items[0].Status)
	}
}

func TestDaemon_BudgetSpentRecordedBlocked(t *testing.T) {
	root := t.TempDir()
	fo := &fakeOrch{result: LoopResult{Outcome: OutcomeBudgetSpent, Iterations: 8}}
	board := progress.NewBoard()
	d := &Daemon{Trigger: fakeTrigger{n: 1}, Orch: fo, Board: board}

	if err := d.Run(context.Background(), func(TriggerSignal) Goal {
		return Goal{Intent: "hard goal", WorkRoot: root}
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	items, _ := board.Load(context.Background(), root)
	if len(items) != 1 || items[0].Status != progress.StatusBlocked {
		t.Errorf("items = %+v, want 1 blocked (budget spent)", items)
	}
}

func TestDaemon_GracefulStopOnCancel(t *testing.T) {
	fo := &fakeOrch{result: LoopResult{Outcome: OutcomeAchieved}}
	d := &Daemon{Trigger: blockingTrigger{}, Orch: fo}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, func(TriggerSignal) Goal { return Goal{Intent: "x"} }) }()
	cancel()

	select {
	case <-done:
		// 取消后应及时返回，无挂起、无泄漏（goleak 兜底）。
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not stop after ctx cancel")
	}
}

func TestDaemon_RequiresTriggerAndOrch(t *testing.T) {
	d := &Daemon{}
	if err := d.Run(context.Background(), func(TriggerSignal) Goal { return Goal{} }); err == nil {
		t.Error("expected error when trigger/orch missing")
	}
}

func TestCronTrigger_FiresAndStops(t *testing.T) {
	ct := CronTrigger{Interval: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	signals, err := ct.Fire(ctx)
	if err != nil {
		t.Fatalf("Fire: %v", err)
	}
	select {
	case sig, ok := <-signals:
		if !ok || sig.Source != "cron" {
			t.Errorf("first signal = %+v ok=%v, want cron", sig, ok)
		}
	case <-time.After(time.Second):
		t.Fatal("cron did not fire")
	}
	cancel()
	// 取消后 channel 应被关闭（goroutine 收尾）。
	deadline := time.After(time.Second)
	for {
		select {
		case _, ok := <-signals:
			if !ok {
				return // 已关闭，符合预期
			}
		case <-deadline:
			t.Fatal("cron channel not closed after cancel")
		}
	}
}

func TestCronTrigger_RejectsNonPositiveInterval(t *testing.T) {
	if _, err := (CronTrigger{Interval: 0}).Fire(context.Background()); err == nil {
		t.Error("expected error for non-positive interval")
	}
}

func TestGoalID(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Fix the parser bug!", "fix-the-parser-bug"},
		{"   ", "goal"},
		{"a/b c", "a-b-c"},
	}
	for _, tt := range tests {
		if got := goalID(tt.in); got != tt.want {
			t.Errorf("goalID(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
