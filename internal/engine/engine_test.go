package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/alaindong/cogent/internal/llm"
	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/types"
)

// TestMain 在包级别断言无 goroutine 泄漏（spec §9.3）。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fakeLLM 是 llm.Client 的脚本化替身：按 Stream 调用次数返回预设增量，
// 并记录每次收到的消息列表与工具声明，供断言多轮历史累积与档位过滤。
type fakeLLM struct {
	mu          sync.Mutex
	turns       [][]llm.Delta
	call        int
	gotMessages [][]types.Message
	gotTools    [][]llm.ToolSchema
	block       chan struct{} // 非 nil 时在发送前阻塞，直至关闭或 ctx 取消（用于取消测试）
}

func (f *fakeLLM) Stream(ctx context.Context, req llm.Request) (<-chan llm.Delta, error) {
	f.mu.Lock()
	idx := f.call
	f.call++
	msgs := append([]types.Message(nil), req.Messages...)
	f.gotMessages = append(f.gotMessages, msgs)
	f.gotTools = append(f.gotTools, append([]llm.ToolSchema(nil), req.Tools...))
	var deltas []llm.Delta
	if idx < len(f.turns) {
		deltas = f.turns[idx]
	}
	block := f.block
	f.mu.Unlock()

	out := make(chan llm.Delta, len(deltas)+1)
	go func() {
		defer close(out)
		if block != nil {
			select {
			case <-ctx.Done():
				return
			case <-block:
			}
		}
		for _, d := range deltas {
			select {
			case <-ctx.Done():
				return
			case out <- d:
			}
		}
	}()
	return out, nil
}

// newTestEngine 用 fake LLM 与 no-op 可观测构造一个可测引擎。
func newTestEngine(t *testing.T, f *fakeLLM) Engine {
	t.Helper()
	prov, err := observe.New(observe.Config{Enabled: false})
	if err != nil {
		t.Fatalf("observe.New: %v", err)
	}
	eng, err := New(Deps{LLM: f, Observe: prov, Model: "test-model"})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	return eng
}

// collect 消费事件流直到关闭，返回事件切片。
func collect(t *testing.T, events <-chan types.StreamEvent) []types.StreamEvent {
	t.Helper()
	var got []types.StreamEvent
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return got
			}
			got = append(got, ev)
		case <-timeout:
			t.Fatal("timed out waiting for events")
		}
	}
}

func TestEngine_PureDialogue(t *testing.T) {
	f := &fakeLLM{turns: [][]llm.Delta{{
		{Text: "Hello"},
		{Text: " world"},
		{Usage: &llm.Usage{PromptTokens: 10, CompletionTokens: 2}},
	}}}
	eng := newTestEngine(t, f)

	events, err := eng.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := collect(t, events)

	var text string
	for _, ev := range got {
		if ev.Type == types.EventText {
			text += ev.Text
		}
	}
	if text != "Hello world" {
		t.Errorf("text = %q, want %q", text, "Hello world")
	}
	if len(got) == 0 || got[len(got)-1].Type != types.EventDone {
		t.Errorf("last event = %v, want EventDone", got)
	}
	for _, ev := range got {
		if ev.Type == types.EventToolStart {
			t.Errorf("unexpected ToolStart in pure dialogue")
		}
	}
}

func TestEngine_MultiTurnHistory(t *testing.T) {
	f := &fakeLLM{turns: [][]llm.Delta{
		{{Text: "a1"}},
		{{Text: "a2"}},
	}}
	eng := newTestEngine(t, f)

	collect(t, mustRun(t, eng, "q1"))
	collect(t, mustRun(t, eng, "q2"))

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.gotMessages) != 2 {
		t.Fatalf("stream calls = %d, want 2", len(f.gotMessages))
	}
	// 第二轮应携带：system + user q1 + assistant a1 + user q2。
	second := f.gotMessages[1]
	if len(second) != 4 {
		t.Fatalf("second turn messages = %d, want 4: %+v", len(second), second)
	}
	wantRoles := []types.Role{types.RoleSystem, types.RoleUser, types.RoleAssistant, types.RoleUser}
	for i, r := range wantRoles {
		if second[i].Role != r {
			t.Errorf("msg[%d].Role = %s, want %s", i, second[i].Role, r)
		}
	}
	if second[2].Text != "a1" {
		t.Errorf("assistant history text = %q, want %q", second[2].Text, "a1")
	}
	if second[3].Text != "q2" {
		t.Errorf("latest user text = %q, want %q", second[3].Text, "q2")
	}
}

func TestEngine_CtxCancel(t *testing.T) {
	f := &fakeLLM{turns: [][]llm.Delta{{{Text: "never sent"}}}, block: make(chan struct{})}
	eng := newTestEngine(t, f)

	ctx, cancel := context.WithCancel(context.Background())
	events, err := eng.Run(ctx, "hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	cancel()

	// 取消后引擎应安全收尾并关闭事件 channel（不挂起）。
	got := collect(t, events)
	for _, ev := range got {
		if ev.Type == types.EventText {
			t.Errorf("unexpected text after cancel: %q", ev.Text)
		}
	}
}

func TestEngine_RunRejectsEmptyTask(t *testing.T) {
	f := &fakeLLM{}
	eng := newTestEngine(t, f)
	if _, err := eng.Run(context.Background(), "   "); err == nil {
		t.Error("expected error for empty task, got nil")
	}
}

// panicLLM 在 Stream 同步阶段 panic，用于验证内核 goroutine 顶层兜底。
type panicLLM struct{}

func (panicLLM) Stream(context.Context, llm.Request) (<-chan llm.Delta, error) {
	panic("synthetic llm panic")
}

func TestEngine_RunRecoversPanic(t *testing.T) {
	prov, _ := observe.New(observe.Config{Enabled: false})
	eng, err := New(Deps{LLM: panicLLM{}, Observe: prov, Model: "m"})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	events, err := eng.Run(context.Background(), "boom")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 单点 panic 不得击穿进程：应降级为 EventError 并正常关闭 channel。
	got := collect(t, events)
	var sawError bool
	for _, ev := range got {
		if ev.Type == types.EventError && ev.Err != nil {
			sawError = true
		}
	}
	if !sawError {
		t.Errorf("expected EventError from recovered panic, got %+v", got)
	}
}

func TestEngine_ResumeRequiresSessionStore(t *testing.T) {
	f := &fakeLLM{}
	eng := newTestEngine(t, f)
	// 未配置 Session 时 Resume 应明确报错（而非静默成功）。
	if _, err := eng.Resume(context.Background(), "sid"); err == nil {
		t.Error("expected resume to require a session store, got nil")
	}
}

func mustRun(t *testing.T, eng Engine, task string) <-chan types.StreamEvent {
	t.Helper()
	events, err := eng.Run(context.Background(), task)
	if err != nil {
		t.Fatalf("Run(%q): %v", task, err)
	}
	return events
}
