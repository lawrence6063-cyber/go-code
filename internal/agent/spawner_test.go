package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/alaindong/cogent/internal/engine"
	"github.com/alaindong/cogent/internal/llm"
	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/types"
)

// TestMain 在包级别断言无 goroutine 泄漏：子 Engine 与其 goroutine 须随 ctx/channel 收尾。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fakeLLM 是 llm.Client 的脚本化替身：按 Stream 调用次数返回预设增量，并记录收到的消息列表，
// 供断言子 Agent 每次派发都从干净历史开始（上下文隔离）。
type fakeLLM struct {
	mu          sync.Mutex
	turns       [][]llm.Delta
	call        int
	gotMessages [][]types.Message
	block       chan struct{} // 非 nil 时发送前阻塞，直至关闭或 ctx 取消（用于取消测试）
}

func (f *fakeLLM) Stream(ctx context.Context, req llm.Request) (<-chan llm.Delta, error) {
	f.mu.Lock()
	idx := f.call
	f.call++
	f.gotMessages = append(f.gotMessages, append([]types.Message(nil), req.Messages...))
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

// newTestSpawner 用 fake LLM 与 no-op 可观测构造一个可测派发器（无工具池）。
func newTestSpawner(t *testing.T, f *fakeLLM) *SubAgent {
	t.Helper()
	prov, err := observe.New(observe.Config{Enabled: false})
	if err != nil {
		t.Fatalf("observe.New: %v", err)
	}
	return New(engine.Deps{LLM: f, Observe: prov, Model: "test-model"})
}

func TestSubAgent_SpawnReturnsSummary(t *testing.T) {
	f := &fakeLLM{turns: [][]llm.Delta{{
		{Text: "found the handler "},
		{Text: "in server.go"},
	}}}
	sp := newTestSpawner(t, f)

	summary, err := sp.Spawn(context.Background(), "locate the request handler")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if summary != "found the handler in server.go" {
		t.Errorf("summary = %q, want %q", summary, "found the handler in server.go")
	}
}

// TestSubAgent_SpawnIsolatesContext 断言每次派发都从干净历史（system+user）开始，
// 子 Agent 之间互不串扰，也不携带父任务历史。
func TestSubAgent_SpawnIsolatesContext(t *testing.T) {
	f := &fakeLLM{turns: [][]llm.Delta{
		{{Text: "first"}},
		{{Text: "second"}},
	}}
	sp := newTestSpawner(t, f)

	if _, err := sp.Spawn(context.Background(), "subtask one"); err != nil {
		t.Fatalf("Spawn 1: %v", err)
	}
	if _, err := sp.Spawn(context.Background(), "subtask two"); err != nil {
		t.Fatalf("Spawn 2: %v", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.gotMessages) != 2 {
		t.Fatalf("stream calls = %d, want 2", len(f.gotMessages))
	}
	for i, msgs := range f.gotMessages {
		if len(msgs) != 2 {
			t.Fatalf("spawn %d messages = %d, want 2 (system+user): %+v", i, len(msgs), msgs)
		}
		if msgs[0].Role != types.RoleSystem || msgs[1].Role != types.RoleUser {
			t.Errorf("spawn %d roles = [%s,%s], want [system,user]", i, msgs[0].Role, msgs[1].Role)
		}
	}
	if f.gotMessages[1][1].Text != "subtask two" {
		t.Errorf("second spawn user text = %q, want %q", f.gotMessages[1][1].Text, "subtask two")
	}
}

func TestSubAgent_SpawnTruncatesLongSummary(t *testing.T) {
	long := strings.Repeat("x", defaultMaxSummaryBytes+100)
	f := &fakeLLM{turns: [][]llm.Delta{{{Text: long}}}}
	sp := newTestSpawner(t, f)

	summary, err := sp.Spawn(context.Background(), "produce a long report")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if !strings.Contains(summary, "truncated") {
		t.Errorf("expected truncation marker in summary")
	}
	if len(summary) > defaultMaxSummaryBytes+64 {
		t.Errorf("summary length = %d, want <= ~%d", len(summary), defaultMaxSummaryBytes)
	}
}

func TestSubAgent_SpawnCtxCancel(t *testing.T) {
	f := &fakeLLM{turns: [][]llm.Delta{{{Text: "never sent"}}}, block: make(chan struct{})}
	sp := newTestSpawner(t, f)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() {
		s, _ := sp.Spawn(ctx, "blocking subtask")
		done <- s
	}()
	cancel()

	select {
	case s := <-done:
		if strings.Contains(s, "never sent") {
			t.Errorf("got blocked text after cancel: %q", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Spawn did not return after ctx cancel (possible hang/leak)")
	}
}

func TestTruncateSummary(t *testing.T) {
	if got := truncateSummary("short", 100); got != "short" {
		t.Errorf("no-truncate = %q, want short", got)
	}
	if got := truncateSummary("0123456789", 4); !strings.HasPrefix(got, "0123") || !strings.Contains(got, "truncated") {
		t.Errorf("truncate = %q, want prefix 0123 + marker", got)
	}
}
