package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alaindong/cogent/internal/agent"
	"github.com/alaindong/cogent/internal/verify"
	"github.com/alaindong/cogent/internal/worktree"
)

// fakeWTManager 记录 worktree 生命周期调用，并可注入 Merge 错误，驱动 worktreePipeline 分支测试。
type fakeWTManager struct {
	created   int
	merged    int
	discarded int
	mergeErr  error
}

func (m *fakeWTManager) Create(context.Context, string) (worktree.Workspace, error) {
	m.created++
	return worktree.Workspace{Root: "/tmp/ws", Branch: "cogent/wt-test"}, nil
}

func (m *fakeWTManager) Merge(context.Context, worktree.Workspace, string) error {
	m.merged++
	return m.mergeErr
}

func (m *fakeWTManager) Discard(context.Context, worktree.Workspace) error {
	m.discarded++
	return nil
}

// fakeRunner 返回预设的双角色裁决，免真实 LLM。
type fakeRunner struct {
	result agent.PipelineResult
	err    error
}

func (r fakeRunner) Iterate(context.Context, string) (agent.PipelineResult, error) {
	return r.result, r.err
}

func newWTPipeline(mgr worktree.Manager, run makerReviewerRunner) *worktreePipeline {
	return &worktreePipeline{
		mgr:     mgr,
		baseRef: "HEAD",
		build:   func(string) makerReviewerRunner { return run },
	}
}

func TestWorktreePipeline_ApprovedMerges(t *testing.T) {
	mgr := &fakeWTManager{}
	run := fakeRunner{result: agent.PipelineResult{
		MakerSummary: "done",
		Verdict:      agent.ReviewVerdict{Approved: true},
	}}
	p := newWTPipeline(mgr, run)

	// verifyAt 为 nil：回退到 reviewer 裁决作为落盘闸门（向后兼容）。
	res, err := p.Iterate(context.Background(), "task", nil)
	if err != nil {
		t.Fatalf("Iterate: %v", err)
	}
	if !res.Approved {
		t.Error("want approved")
	}
	if mgr.merged != 1 || mgr.discarded != 0 {
		t.Errorf("approved should merge once, no discard; merged=%d discarded=%d", mgr.merged, mgr.discarded)
	}
}

func TestWorktreePipeline_RejectedDiscards(t *testing.T) {
	mgr := &fakeWTManager{}
	run := fakeRunner{result: agent.PipelineResult{
		Verdict: agent.ReviewVerdict{Approved: false, Feedback: "fix naming"},
	}}
	p := newWTPipeline(mgr, run)

	res, err := p.Iterate(context.Background(), "task", nil)
	if err != nil {
		t.Fatalf("Iterate: %v", err)
	}
	if res.Approved {
		t.Error("want not approved")
	}
	if res.Feedback != "fix naming" {
		t.Errorf("feedback = %q, want passthrough", res.Feedback)
	}
	if mgr.merged != 0 || mgr.discarded != 1 {
		t.Errorf("rejected should discard once, no merge; merged=%d discarded=%d", mgr.merged, mgr.discarded)
	}
}

func TestWorktreePipeline_MergeConflictDegradesToNotApproved(t *testing.T) {
	mgr := &fakeWTManager{mergeErr: worktree.ErrMergeConflict}
	run := fakeRunner{result: agent.PipelineResult{
		Verdict: agent.ReviewVerdict{Approved: true},
	}}
	p := newWTPipeline(mgr, run)

	res, err := p.Iterate(context.Background(), "task", nil)
	if err != nil {
		t.Fatalf("conflict should degrade, not error: %v", err)
	}
	if res.Approved {
		t.Error("conflict should yield not-approved (continue with feedback)")
	}
	if !strings.Contains(res.Feedback, "merge conflict") {
		t.Errorf("feedback = %q, want conflict note", res.Feedback)
	}
	if mgr.discarded != 1 {
		t.Errorf("conflict should discard worktree; discarded=%d", mgr.discarded)
	}
}

func TestWorktreePipeline_MergeFatalErrorPropagates(t *testing.T) {
	mgr := &fakeWTManager{mergeErr: errors.New("git exploded")}
	run := fakeRunner{result: agent.PipelineResult{
		Verdict: agent.ReviewVerdict{Approved: true},
	}}
	p := newWTPipeline(mgr, run)

	if _, err := p.Iterate(context.Background(), "task", nil); err == nil {
		t.Fatal("non-conflict merge error should propagate")
	}
}

// TestWorktreePipeline_VerifyPassMergesDespiteReviewerReject 断言发现②修复：
// reviewer 拒绝，但客观 verify 通过 → 仍 Merge 落盘（客观为最终硬闸门，不可被主观短路）。
func TestWorktreePipeline_VerifyPassMergesDespiteReviewerReject(t *testing.T) {
	mgr := &fakeWTManager{}
	run := fakeRunner{result: agent.PipelineResult{
		MakerSummary: "done",
		Verdict:      agent.ReviewVerdict{Approved: false, Feedback: "style nit"},
	}}
	p := newWTPipeline(mgr, run)

	var gotRoot string
	verifyAt := func(_ context.Context, workRoot string) verify.Report {
		gotRoot = workRoot
		return verify.Report{Passed: true, Summary: "ok"}
	}
	res, err := p.Iterate(context.Background(), "task", verifyAt)
	if err != nil {
		t.Fatalf("Iterate: %v", err)
	}
	if !res.Approved || res.Report == nil || !res.Report.Passed {
		t.Errorf("verify pass should land as achieved; res=%+v", res)
	}
	if gotRoot != "/tmp/ws" {
		t.Errorf("verifyAt workRoot = %q, want worktree root /tmp/ws", gotRoot)
	}
	if mgr.merged != 1 || mgr.discarded != 0 {
		t.Errorf("verify pass should merge once, no discard; merged=%d discarded=%d", mgr.merged, mgr.discarded)
	}
}

// TestWorktreePipeline_VerifyFailDiscardsDespiteReviewerApprove 断言：
// reviewer 通过，但客观 verify 失败 → Discard（客观判据可否决主观通过），反馈含 verify 与 reviewer 建议。
func TestWorktreePipeline_VerifyFailDiscardsDespiteReviewerApprove(t *testing.T) {
	mgr := &fakeWTManager{}
	run := fakeRunner{result: agent.PipelineResult{
		MakerSummary: "done",
		Verdict:      agent.ReviewVerdict{Approved: true, Feedback: ""},
	}}
	p := newWTPipeline(mgr, run)

	verifyAt := func(context.Context, string) verify.Report {
		return verify.Report{Passed: false, Summary: "go vet failed"}
	}
	res, err := p.Iterate(context.Background(), "task", verifyAt)
	if err != nil {
		t.Fatalf("Iterate: %v", err)
	}
	if res.Approved || res.Report == nil || res.Report.Passed {
		t.Errorf("verify fail should not land; res=%+v", res)
	}
	if !strings.Contains(res.Feedback, "go vet failed") {
		t.Errorf("feedback = %q, want verify summary", res.Feedback)
	}
	if mgr.merged != 0 || mgr.discarded != 1 {
		t.Errorf("verify fail should discard once, no merge; merged=%d discarded=%d", mgr.merged, mgr.discarded)
	}
}
