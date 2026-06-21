package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alaindong/cogent/internal/engine"
	"github.com/alaindong/cogent/internal/llm"
	"github.com/alaindong/cogent/internal/observe"
)

// fakeDiscarder 记录回滚调用次数，用于断言「未通过才回滚」。
type fakeDiscarder struct {
	called int
	err    error
}

func (f *fakeDiscarder) Discard(context.Context) error {
	f.called++
	return f.err
}

// newTestMakerReviewer 用同一 fake LLM 作为 maker(call 0)与 reviewer(call 1)的后端构造流水线。
func newTestMakerReviewer(t *testing.T, f *fakeLLM, d Discarder) *MakerReviewer {
	t.Helper()
	prov, err := observe.New(observe.Config{Enabled: false})
	if err != nil {
		t.Fatalf("observe.New: %v", err)
	}
	deps := engine.Deps{LLM: f, Observe: prov, Model: "test-model"}
	return NewMakerReviewer(deps, deps, d)
}

func TestMakerReviewer_Approved(t *testing.T) {
	f := &fakeLLM{turns: [][]llm.Delta{
		{{Text: "implemented the feature"}}, // maker
		{{Text: "APPROVED"}},                // reviewer
	}}
	d := &fakeDiscarder{}
	mr := newTestMakerReviewer(t, f, d)

	res, err := mr.Iterate(context.Background(), "add feature X")
	if err != nil {
		t.Fatalf("Iterate: %v", err)
	}
	if !res.Verdict.Approved {
		t.Errorf("Approved = false, want true")
	}
	if res.MakerSummary != "implemented the feature" {
		t.Errorf("MakerSummary = %q", res.MakerSummary)
	}
	if d.called != 0 {
		t.Errorf("discarder called %d times on approval, want 0", d.called)
	}
}

func TestMakerReviewer_RejectedDiscards(t *testing.T) {
	f := &fakeLLM{turns: [][]llm.Delta{
		{{Text: "did something"}},
		{{Text: "REJECTED: missing error handling"}},
	}}
	d := &fakeDiscarder{}
	mr := newTestMakerReviewer(t, f, d)

	res, err := mr.Iterate(context.Background(), "add feature Y")
	if err != nil {
		t.Fatalf("Iterate: %v", err)
	}
	if res.Verdict.Approved {
		t.Errorf("Approved = true, want false")
	}
	if !strings.Contains(res.Verdict.Feedback, "missing error handling") {
		t.Errorf("Feedback = %q, want to contain rejection reason", res.Verdict.Feedback)
	}
	if d.called != 1 {
		t.Errorf("discarder called %d times on rejection, want 1", d.called)
	}
}

func TestMakerReviewer_RejectedNilDiscarderSafe(t *testing.T) {
	f := &fakeLLM{turns: [][]llm.Delta{
		{{Text: "did x"}},
		{{Text: "REJECTED: nope"}},
	}}
	mr := newTestMakerReviewer(t, f, nil) // 无 discarder 不应 panic

	res, err := mr.Iterate(context.Background(), "task")
	if err != nil {
		t.Fatalf("Iterate: %v", err)
	}
	if res.Verdict.Approved {
		t.Errorf("Approved = true, want false")
	}
}

// TestMakerReviewer_IndependentContext 断言 reviewer 用独立干净上下文（不携带 maker 历史），
// 且其任务 prompt 含评审 rubric 与 maker 摘要——即「不批改自己的作业」。
func TestMakerReviewer_IndependentContext(t *testing.T) {
	f := &fakeLLM{turns: [][]llm.Delta{
		{{Text: "maker did the work"}},
		{{Text: "APPROVED"}},
	}}
	mr := newTestMakerReviewer(t, f, nil)

	if _, err := mr.Iterate(context.Background(), "refactor module Z"); err != nil {
		t.Fatalf("Iterate: %v", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.gotMessages) != 2 {
		t.Fatalf("stream calls = %d, want 2 (maker + reviewer)", len(f.gotMessages))
	}
	reviewerMsgs := f.gotMessages[1]
	if len(reviewerMsgs) != 2 {
		t.Fatalf("reviewer messages = %d, want 2 (fresh system+user, no maker history)", len(reviewerMsgs))
	}
	reviewTask := reviewerMsgs[1].Text
	if !strings.Contains(reviewTask, "independent code reviewer") {
		t.Errorf("reviewer prompt missing rubric: %q", reviewTask)
	}
	if !strings.Contains(reviewTask, "maker did the work") {
		t.Errorf("reviewer prompt missing maker summary")
	}
	if !strings.Contains(reviewTask, "refactor module Z") {
		t.Errorf("reviewer prompt missing original task")
	}
}

func TestMakerReviewer_MakerErrorPropagates(t *testing.T) {
	f := &fakeLLM{turns: [][]llm.Delta{
		{{Err: errors.New("llm boom")}}, // maker stream errors
	}}
	mr := newTestMakerReviewer(t, f, nil)
	// maker 内层错误规范为摘要文本（engine 不抛 error），故 Iterate 仍成功返回，
	// reviewer 据此审查；这里只断言不挂起、不 panic。
	if _, err := mr.Iterate(context.Background(), "task"); err != nil {
		t.Fatalf("Iterate should not hard-fail on inner error: %v", err)
	}
}

func TestParseVerdict(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		approved bool
	}{
		{"plain approved", "APPROVED", true},
		{"approved lowercase", "approved\nlooks good", true},
		{"approved with leading blank", "\n\nAPPROVED", true},
		{"rejected", "REJECTED: needs tests", false},
		{"empty fail-closed", "", false},
		{"garbage fail-closed", "I think maybe it is fine?", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := parseVerdict(tt.in)
			if v.Approved != tt.approved {
				t.Errorf("parseVerdict(%q).Approved = %v, want %v", tt.in, v.Approved, tt.approved)
			}
			if !v.Approved && v.Feedback == "" {
				t.Errorf("rejected verdict must carry feedback")
			}
		})
	}
}
