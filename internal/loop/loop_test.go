package loop

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/alaindong/cogent/internal/types"
	"github.com/alaindong/cogent/internal/verify"
)

// TestMain 在包级别断言无 goroutine 泄漏（spec §9.3）。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fakeEngine 是 engine.Engine 的脚本化替身：按 Run 调用次数返回预设事件流，
// 记录每次收到的 task（供断言带反馈续跑），可选阻塞用于取消测试。
type fakeEngine struct {
	mu       sync.Mutex
	turns    [][]types.StreamEvent
	gotTasks []string
	call     int
	block    chan struct{}
}

func (f *fakeEngine) Run(ctx context.Context, task string) (<-chan types.StreamEvent, error) {
	f.mu.Lock()
	idx := f.call
	f.call++
	f.gotTasks = append(f.gotTasks, task)
	var evs []types.StreamEvent
	if idx < len(f.turns) {
		evs = f.turns[idx]
	}
	block := f.block
	f.mu.Unlock()

	out := make(chan types.StreamEvent, len(evs)+1)
	go func() {
		defer close(out)
		if block != nil {
			select {
			case <-ctx.Done():
				return
			case <-block:
			}
		}
		for _, e := range evs {
			select {
			case <-ctx.Done():
				return
			case out <- e:
			}
		}
	}()
	return out, nil
}

func (f *fakeEngine) Resume(context.Context, string) (<-chan types.StreamEvent, error) {
	return nil, errors.New("resume not supported in fake")
}

func (f *fakeEngine) tasks() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.gotTasks...)
}

// fakeVerifier 是 verify.Verifier 的脚本化替身：按调用次数返回预设报告。
type fakeVerifier struct {
	mu      sync.Mutex
	reports []verify.Report
	err     error
	call    int
}

func (f *fakeVerifier) Verify(context.Context, string, string) (verify.Report, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := f.call
	f.call++
	if f.err != nil {
		return verify.Report{}, f.err
	}
	if idx < len(f.reports) {
		return f.reports[idx], nil
	}
	return verify.Report{Summary: "not passed (default)"}, nil
}

// fakeCostMeter 返回固定的累计成本，用于驱动成本护栏测试。
type fakeCostMeter struct{ spent float64 }

func (f fakeCostMeter) SpentUSD() float64 { return f.spent }

// passed / failed 是构造判定报告的便捷函数。
func passed() verify.Report { return verify.Report{Passed: true, Summary: "ok"} }
func failed() verify.Report { return verify.Report{Summary: "still failing"} }

// doneTurn 返回一个「文本 + 结束」的内层事件序列，模拟 engine 跑完一轮。
func doneTurn(text string) []types.StreamEvent {
	return []types.StreamEvent{{Type: types.EventText, Text: text}, {Type: types.EventDone}}
}

func newOrch(t *testing.T, fe *fakeEngine, cost CostMeter) Orchestrator {
	t.Helper()
	o, err := New(Deps{Engine: fe, Cost: cost})
	if err != nil {
		t.Fatalf("loop.New: %v", err)
	}
	return o
}

func collectLoop(t *testing.T, events <-chan LoopEvent) []LoopEvent {
	t.Helper()
	var got []LoopEvent
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return got
			}
			got = append(got, ev)
		case <-timeout:
			t.Fatal("timed out waiting for loop events")
		}
	}
}

func lastResult(t *testing.T, got []LoopEvent) LoopResult {
	t.Helper()
	for i := len(got) - 1; i >= 0; i-- {
		if got[i].Type == LoopFinished && got[i].Result != nil {
			return *got[i].Result
		}
	}
	t.Fatal("no LoopFinished event with result")
	return LoopResult{}
}

func countType(got []LoopEvent, typ LoopEventType) int {
	n := 0
	for _, ev := range got {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

func TestRunGoal_AchievedFirstIteration(t *testing.T) {
	fe := &fakeEngine{turns: [][]types.StreamEvent{doneTurn("done")}}
	fv := &fakeVerifier{reports: []verify.Report{passed()}}
	o := newOrch(t, fe, nil)

	events, err := o.RunGoal(context.Background(), Goal{
		Intent:   "fix the bug",
		Verifier: fv,
		Budget:   Budget{MaxIterations: 5},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	got := collectLoop(t, events)

	res := lastResult(t, got)
	if res.Outcome != OutcomeAchieved {
		t.Errorf("Outcome = %v, want Achieved", res.Outcome)
	}
	if res.Iterations != 1 {
		t.Errorf("Iterations = %d, want 1", res.Iterations)
	}
	if countType(got, LoopIterationStart) != 1 {
		t.Errorf("IterationStart count = %d, want 1", countType(got, LoopIterationStart))
	}
	if got := fe.tasks(); len(got) != 1 || got[0] != "fix the bug" {
		t.Errorf("engine tasks = %v, want [fix the bug]", got)
	}
}

func TestRunGoal_MultiRoundFeedback(t *testing.T) {
	fe := &fakeEngine{turns: [][]types.StreamEvent{
		doneTurn("try1"), doneTurn("try2"), doneTurn("try3"),
	}}
	fv := &fakeVerifier{reports: []verify.Report{failed(), failed(), passed()}}
	o := newOrch(t, fe, nil)

	events, err := o.RunGoal(context.Background(), Goal{
		Intent:   "make tests green",
		Verifier: fv,
		Budget:   Budget{MaxIterations: 5},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	got := collectLoop(t, events)

	res := lastResult(t, got)
	if res.Outcome != OutcomeAchieved || res.Iterations != 3 {
		t.Errorf("got Outcome=%v Iterations=%d, want Achieved/3", res.Outcome, res.Iterations)
	}
	tasks := fe.tasks()
	if len(tasks) != 3 {
		t.Fatalf("engine called %d times, want 3", len(tasks))
	}
	if tasks[0] != "make tests green" {
		t.Errorf("first task = %q, want original intent", tasks[0])
	}
	// 第 2、3 轮 task 应为反馈续跑：含原始目标 + 未达标提示。
	for i := 1; i < 3; i++ {
		if !strings.Contains(tasks[i], "make tests green") ||
			!strings.Contains(tasks[i], "not yet achieved") {
			t.Errorf("task[%d] = %q, want feedback prompt with intent", i, tasks[i])
		}
	}
}

func TestRunGoal_BudgetIterationsSpent(t *testing.T) {
	fe := &fakeEngine{turns: [][]types.StreamEvent{
		doneTurn("a"), doneTurn("b"), doneTurn("c"), doneTurn("d"),
	}}
	fv := &fakeVerifier{reports: []verify.Report{failed(), failed(), failed(), failed()}}
	o := newOrch(t, fe, nil)

	events, err := o.RunGoal(context.Background(), Goal{
		Intent:   "never passes",
		Verifier: fv,
		Budget:   Budget{MaxIterations: 3},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	got := collectLoop(t, events)

	res := lastResult(t, got)
	if res.Outcome != OutcomeBudgetSpent || res.Iterations != 3 {
		t.Errorf("got Outcome=%v Iterations=%d, want BudgetSpent/3", res.Outcome, res.Iterations)
	}
	if n := len(fe.tasks()); n != 3 {
		t.Errorf("engine called %d times, want exactly 3 (no infinite loop)", n)
	}
}

func TestRunGoal_BudgetCostSpent(t *testing.T) {
	fe := &fakeEngine{turns: [][]types.StreamEvent{doneTurn("x"), doneTurn("y")}}
	fv := &fakeVerifier{reports: []verify.Report{failed(), failed()}}
	o := newOrch(t, fe, fakeCostMeter{spent: 9})

	events, err := o.RunGoal(context.Background(), Goal{
		Intent:   "expensive",
		Verifier: fv,
		Budget:   Budget{MaxIterations: 10, MaxCostUSD: 1},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	got := collectLoop(t, events)

	res := lastResult(t, got)
	if res.Outcome != OutcomeBudgetSpent {
		t.Errorf("Outcome = %v, want BudgetSpent (cost)", res.Outcome)
	}
	if res.Iterations != 1 {
		t.Errorf("Iterations = %d, want 1 (cost cap hit after first round)", res.Iterations)
	}
}

func TestRunGoal_NilVerifierFailClosed(t *testing.T) {
	fe := &fakeEngine{turns: [][]types.StreamEvent{doneTurn("a"), doneTurn("b")}}
	o := newOrch(t, fe, nil)

	events, err := o.RunGoal(context.Background(), Goal{
		Intent: "no verifier",
		Budget: Budget{MaxIterations: 2},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	got := collectLoop(t, events)

	res := lastResult(t, got)
	if res.Outcome != OutcomeBudgetSpent || res.Iterations != 2 {
		t.Errorf("got Outcome=%v Iterations=%d, want BudgetSpent/2", res.Outcome, res.Iterations)
	}
}

func TestRunGoal_CtxCancel(t *testing.T) {
	fe := &fakeEngine{
		turns: [][]types.StreamEvent{doneTurn("never")},
		block: make(chan struct{}),
	}
	fv := &fakeVerifier{reports: []verify.Report{passed()}}
	o := newOrch(t, fe, nil)

	ctx, cancel := context.WithCancel(context.Background())
	events, err := o.RunGoal(ctx, Goal{Intent: "hang", Verifier: fv, Budget: Budget{MaxIterations: 3}})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	cancel()

	// 取消后应安全收尾并关闭事件 channel（不挂起、无泄漏）。
	got := collectLoop(t, events)
	for _, ev := range got {
		if ev.Type == LoopVerify {
			t.Errorf("unexpected verify after cancel before inner finished")
		}
	}
}

func TestRunGoal_EmptyIntent(t *testing.T) {
	fe := &fakeEngine{}
	o := newOrch(t, fe, nil)
	if _, err := o.RunGoal(context.Background(), Goal{Intent: "   "}); err == nil {
		t.Error("expected error for empty intent, got nil")
	}
}

func TestNew_NilEngine(t *testing.T) {
	if _, err := New(Deps{}); err == nil {
		t.Error("expected error for nil engine, got nil")
	}
}

func TestDefaultBudget(t *testing.T) {
	b := DefaultBudget()
	if b.MaxIterations != 8 || b.MaxCostUSD != 5 || b.MaxWallClock != 15*time.Minute {
		t.Errorf("DefaultBudget = %+v, want 8/5/15m", b)
	}
}

func TestWithDefaults(t *testing.T) {
	if got := withDefaults(Budget{}); got != DefaultBudget() {
		t.Errorf("withDefaults(zero) = %+v, want DefaultBudget", got)
	}
	// 仅设成本上限时应补全保守轮数护栏，避免 0 轮直接退出。
	got := withDefaults(Budget{MaxCostUSD: 2})
	if got.MaxIterations != DefaultBudget().MaxIterations {
		t.Errorf("MaxIterations = %d, want default fill", got.MaxIterations)
	}
}

// fakePipeline 是 Pipeline 的脚本化替身：按调用次数返回预设的「制造-审查」产出。
type fakePipeline struct {
	mu       sync.Mutex
	results  []PipelineResult
	gotTasks []string
	call     int
}

func (f *fakePipeline) Iterate(_ context.Context, task string) (PipelineResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotTasks = append(f.gotTasks, task)
	idx := f.call
	f.call++
	if idx < len(f.results) {
		return f.results[idx], nil
	}
	return PipelineResult{Approved: false, Feedback: "default reject"}, nil
}

func (f *fakePipeline) tasks() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.gotTasks...)
}

func newPipelineOrch(t *testing.T, fp Pipeline, cost CostMeter) Orchestrator {
	t.Helper()
	o, err := New(Deps{Pipeline: fp, Cost: cost})
	if err != nil {
		t.Fatalf("loop.New(pipeline): %v", err)
	}
	return o
}

// TestRunGoal_PipelineRejectThenApprove：reviewer 首轮拒（带反馈续跑），次轮过→客观验收通过→达标。
func TestRunGoal_PipelineRejectThenApprove(t *testing.T) {
	fp := &fakePipeline{results: []PipelineResult{
		{Summary: "attempt 1", Approved: false, Feedback: "needs error handling"},
		{Summary: "attempt 2", Approved: true},
	}}
	fv := &fakeVerifier{reports: []verify.Report{passed()}} // 仅在 reviewer 通过后被调用
	o := newPipelineOrch(t, fp, nil)

	events, err := o.RunGoal(context.Background(), Goal{
		Intent:   "implement feature",
		Verifier: fv,
		Budget:   Budget{MaxIterations: 5},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	got := collectLoop(t, events)

	res := lastResult(t, got)
	if res.Outcome != OutcomeAchieved || res.Iterations != 2 {
		t.Errorf("got Outcome=%v Iterations=%d, want Achieved/2", res.Outcome, res.Iterations)
	}
	tasks := fp.tasks()
	if len(tasks) != 2 {
		t.Fatalf("pipeline called %d times, want 2", len(tasks))
	}
	if !strings.Contains(tasks[1], "reviewer rejected") || !strings.Contains(tasks[1], "needs error handling") {
		t.Errorf("second task missing reviewer feedback: %q", tasks[1])
	}
	if fv.call != 1 {
		t.Errorf("verifier called %d times, want 1 (only after reviewer approves)", fv.call)
	}
}

// TestRunGoal_PipelineApproveButVerifierFails：reviewer 通过但客观验收失败=未达标（双闸门）。
func TestRunGoal_PipelineApproveButVerifierFails(t *testing.T) {
	fp := &fakePipeline{results: []PipelineResult{
		{Summary: "a", Approved: true},
		{Summary: "b", Approved: true},
	}}
	fv := &fakeVerifier{reports: []verify.Report{failed(), failed()}}
	o := newPipelineOrch(t, fp, nil)

	events, err := o.RunGoal(context.Background(), Goal{
		Intent:   "subtly wrong",
		Verifier: fv,
		Budget:   Budget{MaxIterations: 2},
	})
	if err != nil {
		t.Fatalf("RunGoal: %v", err)
	}
	got := collectLoop(t, events)

	res := lastResult(t, got)
	if res.Outcome != OutcomeBudgetSpent {
		t.Errorf("Outcome = %v, want BudgetSpent (reviewer ok but tests fail)", res.Outcome)
	}
	if fv.call != 2 {
		t.Errorf("verifier called %d times, want 2", fv.call)
	}
}
