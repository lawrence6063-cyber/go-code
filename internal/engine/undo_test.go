package engine

import (
	"context"
	"testing"

	"github.com/alaindong/cogent/internal/llm"
	"github.com/alaindong/cogent/internal/observe"
)

func TestEngine_Undo_Basic(t *testing.T) {
	// 两轮对话后 undo 一轮，验证消息历史正确截断
	f := &fakeLLM{turns: [][]llm.Delta{
		{{Text: "reply1"}},
		{{Text: "reply2"}},
	}}
	eng := newTestEngine(t, f)

	collect(t, mustRun(t, eng, "q1"))
	collect(t, mustRun(t, eng, "q2"))

	result, err := eng.Undo(context.Background())
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if result.Summary != "q2" {
		t.Errorf("Summary = %q, want %q", result.Summary, "q2")
	}
	if result.RemovedCount != 2 { // user q2 + assistant reply2
		t.Errorf("RemovedCount = %d, want 2", result.RemovedCount)
	}
	if result.HasFileChanges {
		t.Error("HasFileChanges = true, want false (no snapshotter)")
	}
}

func TestEngine_Undo_ConsecutiveMultiple(t *testing.T) {
	// 三轮对话后连续 undo 三次，验证逐轮回退直到返回 ErrNothingToUndo
	f := &fakeLLM{turns: [][]llm.Delta{
		{{Text: "a1"}},
		{{Text: "a2"}},
		{{Text: "a3"}},
	}}
	eng := newTestEngine(t, f)

	collect(t, mustRun(t, eng, "q1"))
	collect(t, mustRun(t, eng, "q2"))
	collect(t, mustRun(t, eng, "q3"))

	// 第一次 undo：回退 q3
	r1, err := eng.Undo(context.Background())
	if err != nil {
		t.Fatalf("Undo 1: %v", err)
	}
	if r1.Summary != "q3" {
		t.Errorf("Undo 1 Summary = %q, want %q", r1.Summary, "q3")
	}

	// 第二次 undo：回退 q2
	r2, err := eng.Undo(context.Background())
	if err != nil {
		t.Fatalf("Undo 2: %v", err)
	}
	if r2.Summary != "q2" {
		t.Errorf("Undo 2 Summary = %q, want %q", r2.Summary, "q2")
	}

	// 第三次 undo：回退 q1
	r3, err := eng.Undo(context.Background())
	if err != nil {
		t.Fatalf("Undo 3: %v", err)
	}
	if r3.Summary != "q1" {
		t.Errorf("Undo 3 Summary = %q, want %q", r3.Summary, "q1")
	}

	// 第四次 undo：应返回 ErrNothingToUndo
	_, err = eng.Undo(context.Background())
	if err != ErrNothingToUndo {
		t.Errorf("Undo 4 err = %v, want ErrNothingToUndo", err)
	}
}

func TestEngine_Undo_NothingToUndo(t *testing.T) {
	// 刚启动的 engine 没有任何轮次，undo 应返回 ErrNothingToUndo
	f := &fakeLLM{}
	eng := newTestEngine(t, f)

	_, err := eng.Undo(context.Background())
	if err != ErrNothingToUndo {
		t.Errorf("err = %v, want ErrNothingToUndo", err)
	}
}

func TestEngine_Undo_ThenNewTurn(t *testing.T) {
	// undo 后发起新对话，验证新轮次基于回退后的历史
	f := &fakeLLM{turns: [][]llm.Delta{
		{{Text: "a1"}},
		{{Text: "a2"}},
		{{Text: "a3"}},
	}}
	eng := newTestEngine(t, f)

	collect(t, mustRun(t, eng, "q1"))
	collect(t, mustRun(t, eng, "q2"))

	// undo q2
	if _, err := eng.Undo(context.Background()); err != nil {
		t.Fatalf("Undo: %v", err)
	}

	// 发起新轮次 q3
	collect(t, mustRun(t, eng, "q3"))

	// 验证第三次 LLM 调用的消息历史：system + q1 + a1 + q3（不含 q2/a2）
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.gotMessages) != 3 {
		t.Fatalf("stream calls = %d, want 3", len(f.gotMessages))
	}
	third := f.gotMessages[2]
	// system + user(q1) + assistant(a1) + user(q3) = 4
	if len(third) != 4 {
		t.Fatalf("third turn messages = %d, want 4: %+v", len(third), third)
	}
	if third[3].Text != "q3" {
		t.Errorf("last msg text = %q, want %q", third[3].Text, "q3")
	}
}

func TestEngine_Undo_WithSnapshotter(t *testing.T) {
	// 验证带 Snapshotter 时 Undo 会调用 Restore
	snap := &fakeSnapshotter{isGit: true}
	f := &fakeLLM{turns: [][]llm.Delta{{{Text: "reply"}}}}
	prov, err := observe.New(observe.Config{Enabled: false})
	if err != nil {
		t.Fatalf("observe.New: %v", err)
	}
	eng, err := New(Deps{LLM: f, Observe: prov, Model: "test", Snapshotter: snap})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	collect(t, mustRun(t, eng, "hello"))

	result, err := eng.Undo(context.Background())
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if !result.HasFileChanges {
		t.Error("HasFileChanges = false, want true")
	}
	if snap.restoreCalls != 1 {
		t.Errorf("Restore calls = %d, want 1", snap.restoreCalls)
	}
}

func TestEngine_Undo_SummaryTruncation(t *testing.T) {
	// 验证超长 user 消息的摘要被截断到 50 字符
	longMsg := "This is a very long message that exceeds fifty characters limit for summary"
	f := &fakeLLM{turns: [][]llm.Delta{{{Text: "ok"}}}}
	eng := newTestEngine(t, f)

	collect(t, mustRun(t, eng, longMsg))

	result, err := eng.Undo(context.Background())
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if len(result.Summary) > 54 { // 50 + "..."
		t.Errorf("Summary too long: %d chars", len(result.Summary))
	}
	if result.Summary != longMsg[:50]+"..." {
		t.Errorf("Summary = %q, want %q", result.Summary, longMsg[:50]+"...")
	}
}

// fakeSnapshotter 是 Snapshotter 接口的测试替身。
type fakeSnapshotter struct {
	isGit        bool
	takeCalls    int
	restoreCalls int
	takeID       string
}

func (f *fakeSnapshotter) Take(_ context.Context) (string, error) {
	f.takeCalls++
	return f.takeID, nil
}

func (f *fakeSnapshotter) Restore(_ context.Context, _ string) error {
	f.restoreCalls++
	return nil
}

func (f *fakeSnapshotter) IsGitRepo() bool {
	return f.isGit
}
