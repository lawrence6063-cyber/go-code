package eval

import (
	"bytes"
	"testing"
	"time"

	"github.com/alaindong/cogent/internal/loop"
)

func TestLoadReportRoundTrip(t *testing.T) {
	orig := Report{
		Suite: "native", Total: 2, GeneratedAt: time.Unix(100, 0).UTC(),
		Metrics: Metrics{Total: 2, Passed: 1, SuccessRate: 0.5},
		ByGroup: map[string]Metrics{"difficulty=easy": {Total: 1, Passed: 1, SuccessRate: 1}},
		Cases: []CaseResult{
			{ID: "native/a", Outcome: loop.OutcomeAchieved, ExpectedOutcome: "achieved", Pass: true, Iterations: 2, SpentUSD: 0.3, Elapsed: 3 * time.Second},
			{ID: "native/b", Outcome: loop.OutcomeBudgetSpent, ExpectedOutcome: "budget_spent", Pass: false, Iterations: 5},
		},
	}
	var buf bytes.Buffer
	if err := orig.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	got, err := LoadReport(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 2 || got.Metrics.Passed != 1 {
		t.Fatalf("report scalar mismatch: %+v", got)
	}
	if len(got.Cases) != 2 {
		t.Fatalf("cases len=%d, want 2", len(got.Cases))
	}
	a := got.Cases[0]
	if a.ID != "native/a" || a.Outcome != loop.OutcomeAchieved || !a.Pass || a.Iterations != 2 {
		t.Fatalf("case a mismatch: %+v", a)
	}
	if a.Elapsed != 3*time.Second {
		t.Fatalf("case a elapsed=%v, want 3s", a.Elapsed)
	}
	if got.Cases[1].Outcome != loop.OutcomeBudgetSpent {
		t.Fatalf("case b outcome mismatch: %v", got.Cases[1].Outcome)
	}
}

func TestCompareRegressionAndFix(t *testing.T) {
	base := Report{
		Metrics: Metrics{SuccessRate: 1.0},
		Cases: []CaseResult{
			{ID: "native/a", Pass: true},  // 将退化
			{ID: "native/b", Pass: false}, // 将改善
			{ID: "native/c", Pass: true},  // 保持
		},
	}
	head := Report{
		Metrics: Metrics{SuccessRate: 0.66},
		Cases: []CaseResult{
			{ID: "native/a", Pass: false}, // 退化
			{ID: "native/b", Pass: true},  // 改善
			{ID: "native/c", Pass: true},  // 保持
		},
	}
	cmp := Compare(base, head)
	if !cmp.HasRegression() {
		t.Fatal("expected regression")
	}
	if len(cmp.Regressed) != 1 || cmp.Regressed[0] != "native/a" {
		t.Fatalf("Regressed=%v, want [native/a]", cmp.Regressed)
	}
	if len(cmp.Fixed) != 1 || cmp.Fixed[0] != "native/b" {
		t.Fatalf("Fixed=%v, want [native/b]", cmp.Fixed)
	}
	// 指标 delta：Success Rate 0.66 - 1.0 < 0。
	var found bool
	for _, m := range cmp.Metrics {
		if m.Name == "Task Success Rate" {
			found = true
			if m.Delta() >= 0 {
				t.Errorf("success rate delta should be negative, got %.4f", m.Delta())
			}
		}
	}
	if !found {
		t.Error("Task Success Rate metric delta missing")
	}
}

func TestCompareNoRegression(t *testing.T) {
	base := Report{Cases: []CaseResult{{ID: "x", Pass: true}}}
	head := Report{Cases: []CaseResult{{ID: "x", Pass: true}}}
	if Compare(base, head).HasRegression() {
		t.Fatal("no regression expected")
	}
}

func TestComparisonMarkdown(t *testing.T) {
	cmp := Compare(
		Report{Cases: []CaseResult{{ID: "native/a", Pass: true}}},
		Report{Cases: []CaseResult{{ID: "native/a", Pass: false}}},
	)
	var buf bytes.Buffer
	if err := cmp.WriteMarkdown(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"# cogent eval compare", "指标 delta", "native/a"} {
		if !bytes.Contains(buf.Bytes(), []byte(want)) {
			t.Errorf("markdown missing %q\n%s", want, out)
		}
	}
}
