package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alaindong/cogent/internal/eval/adapter"
	"github.com/alaindong/cogent/internal/loop"
	"github.com/alaindong/cogent/internal/verify"
)

// fakeExecutor 返回按 case id 预置的 LoopResult / error，免真实 LLM（EVAL_SPEC §6.9）。
type fakeExecutor struct {
	byID map[string]struct {
		res loop.LoopResult
		err error
	}
	seenArt map[string]string // 记录每个 case 拿到的 artifact 目录，供断言
}

func (f *fakeExecutor) Run(_ context.Context, c adapter.Case, art string) (loop.LoopResult, error) {
	if f.seenArt != nil {
		f.seenArt[c.ID] = art
	}
	v := f.byID[c.ID]
	return v.res, v.err
}

func mkCase(id, difficulty, expected string, budgetIters int, caps ...string) adapter.Case {
	return adapter.Case{
		ID:              id,
		Goal:            loop.Goal{Budget: loop.Budget{MaxIterations: budgetIters}},
		Meta:            adapter.Meta{Difficulty: difficulty, Languages: []string{"go"}, Capabilities: caps, Source: "native"},
		ExpectedOutcome: expected,
	}
}

func TestJudgePass(t *testing.T) {
	tests := []struct {
		name     string
		expected string
		budget   loop.Budget
		res      loop.LoopResult
		want     bool
	}{
		{"achieved-ok", "achieved", loop.Budget{}, loop.LoopResult{Outcome: loop.OutcomeAchieved}, true},
		{"achieved-no", "achieved", loop.Budget{}, loop.LoopResult{Outcome: loop.OutcomeBudgetSpent}, false},
		{"empty-defaults-achieved", "", loop.Budget{}, loop.LoopResult{Outcome: loop.OutcomeAchieved}, true},
		{"budget-ok", "budget_spent", loop.Budget{MaxIterations: 3}, loop.LoopResult{Outcome: loop.OutcomeBudgetSpent, Iterations: 3}, true},
		{"budget-overrun", "budget_spent", loop.Budget{MaxIterations: 3}, loop.LoopResult{Outcome: loop.OutcomeBudgetSpent, Iterations: 4}, false},
		{"budget-wrong-outcome", "budget_spent", loop.Budget{MaxIterations: 3}, loop.LoopResult{Outcome: loop.OutcomeAchieved, Iterations: 1}, false},
		{"canceled-ok", "canceled", loop.Budget{}, loop.LoopResult{Outcome: loop.OutcomeCanceled}, true},
		{"fatal-never", "achieved", loop.Budget{}, loop.LoopResult{Outcome: loop.OutcomeFatal}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := judgePass(tc.expected, tc.budget, tc.res); got != tc.want {
				t.Fatalf("judgePass=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestRunnerAggregates(t *testing.T) {
	cases := []adapter.Case{
		mkCase("native/a", "easy", "achieved", 8, "convergence"),
		mkCase("native/b", "medium", "achieved", 8, "convergence"),
		mkCase("native/c", "medium", "budget_spent", 3, "budget"),
		mkCase("native/d", "hard", "achieved", 8, "review"),
	}
	fe := &fakeExecutor{byID: map[string]struct {
		res loop.LoopResult
		err error
	}{
		"native/a": {res: loop.LoopResult{Outcome: loop.OutcomeAchieved, Iterations: 1, SpentUSD: 0.1, LastReport: reportPassed(true)}},
		"native/b": {res: loop.LoopResult{Outcome: loop.OutcomeAchieved, Iterations: 3, SpentUSD: 0.5, LastReport: reportPassed(true)}},
		"native/c": {res: loop.LoopResult{Outcome: loop.OutcomeBudgetSpent, Iterations: 3, SpentUSD: 0.2}},
		"native/d": {err: errors.New("boom")}, // fatal
	}}
	rep, err := NewRunner().Run(context.Background(), cases, RunOptions{Executor: fe})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Total != 4 {
		t.Fatalf("Total=%d, want 4", rep.Total)
	}
	// a(achieved✓) b(achieved✓) c(budget_spent✓) d(fatal✗) → Passed=3
	if rep.Metrics.Passed != 3 {
		t.Errorf("Passed=%d, want 3", rep.Metrics.Passed)
	}
	if rep.Metrics.Pass1 != 1 { // only a passed with 1 iter
		t.Errorf("Pass1=%d, want 1", rep.Metrics.Pass1)
	}
	if rep.Metrics.FatalCount != 1 {
		t.Errorf("FatalCount=%d, want 1", rep.Metrics.FatalCount)
	}
	// AvgIterationsToGreen over achieved passes a(1) + b(3) = 2.0
	if rep.Metrics.AvgIterationsToGreen != 2.0 {
		t.Errorf("AvgIterationsToGreen=%.2f, want 2.0", rep.Metrics.AvgIterationsToGreen)
	}
	// 分组：capability=budget 组 1/1 通过。
	if g := rep.ByGroup["capability=budget"]; g.Total != 1 || g.Passed != 1 {
		t.Errorf("capability=budget group=%+v, want 1/1", g)
	}
	if g := rep.ByGroup["difficulty=medium"]; g.Total != 2 {
		t.Errorf("difficulty=medium total=%d, want 2", g.Total)
	}
}

func TestRunnerNilExecutor(t *testing.T) {
	if _, err := NewRunner().Run(context.Background(), nil, RunOptions{}); err == nil {
		t.Fatal("want error on nil executor")
	}
}

func TestRunnerBudgetOverride(t *testing.T) {
	c := mkCase("native/x", "easy", "achieved", 8, "convergence")
	fe := &fakeExecutor{byID: map[string]struct {
		res loop.LoopResult
		err error
	}{"native/x": {res: loop.LoopResult{Outcome: loop.OutcomeAchieved, Iterations: 1}}}}
	override := loop.Budget{MaxIterations: 2, MaxCostUSD: 1, MaxWallClock: time.Minute}
	// 用一个记录预算的 executor 断言覆盖生效。
	rec := &budgetRecorder{fakeExecutor: fe}
	if _, err := NewRunner().Run(context.Background(), []adapter.Case{c}, RunOptions{Executor: rec, Budget: override}); err != nil {
		t.Fatal(err)
	}
	if rec.gotBudget.MaxIterations != 2 {
		t.Fatalf("budget override not applied: %+v", rec.gotBudget)
	}
}

type budgetRecorder struct {
	*fakeExecutor
	gotBudget loop.Budget
}

func (b *budgetRecorder) Run(ctx context.Context, c adapter.Case, art string) (loop.LoopResult, error) {
	b.gotBudget = c.Goal.Budget
	return b.fakeExecutor.Run(ctx, c, art)
}

func TestReportJSONRoundTrip(t *testing.T) {
	rep := Report{
		Suite: "native", Total: 1,
		Metrics: Metrics{Total: 1, Passed: 1, SuccessRate: 1},
		Cases: []CaseResult{{
			ID: "native/a", Outcome: loop.OutcomeBudgetSpent, ExpectedOutcome: "budget_spent",
			Pass: true, Iterations: 3, SpentUSD: 0.2, Elapsed: 2 * time.Second,
		}},
	}
	var buf bytes.Buffer
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	// outcome 应序列化为字符串而非 int 枚举。
	var parsed map[string]any
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	cs := parsed["cases"].([]any)[0].(map[string]any)
	if cs["outcome"] != "budget_spent" {
		t.Fatalf("outcome not string-encoded: %v", cs["outcome"])
	}
	if cs["elapsed_sec"].(float64) != 2.0 {
		t.Fatalf("elapsed_sec=%v, want 2", cs["elapsed_sec"])
	}
}

func TestReportMarkdown(t *testing.T) {
	rep := Report{
		Suite: "native", Total: 2, GeneratedAt: time.Unix(0, 0).UTC(),
		Metrics: Metrics{Total: 2, Passed: 1, SuccessRate: 0.5, Pass1: 1, Pass1Rate: 0.5},
		ByGroup: map[string]Metrics{"difficulty=easy": {Total: 1, Passed: 1, SuccessRate: 1}},
		Cases: []CaseResult{
			{ID: "native/a", Outcome: loop.OutcomeAchieved, ExpectedOutcome: "achieved", Pass: true},
			{ID: "native/b", Outcome: loop.OutcomeFatal, ExpectedOutcome: "achieved", Pass: false},
		},
	}
	var buf bytes.Buffer
	if err := rep.WriteMarkdown(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"# cogent eval report", "Task Success Rate | 1/2 (50.0%)", "difficulty=easy", "native/b"} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown missing %q\n---\n%s", want, out)
		}
	}
	// 通过的样本 native/a 不应出现在失败清单里。
	if strings.Contains(out, "| native/a | achieved") {
		t.Error("passed case leaked into failure list")
	}
}

func reportPassed(p bool) verify.Report { return verify.Report{Passed: p} }
