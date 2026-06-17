package engine

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/alaindong/cogent/internal/contextmgr"
	"github.com/alaindong/cogent/internal/llm"
	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/tool"
	"github.com/alaindong/cogent/internal/types"
)

// compactLLM 区分「ReAct 推理调用」与「压缩摘要调用」：
// 首个 ReAct 调用返回一个工具调用 + 超大 usage（触发压缩），其后返回收尾文本；
// 摘要调用（系统提示含 "compacting"）返回固定摘要文本。
type compactLLM struct {
	mu           sync.Mutex
	reactIdx     int
	reactMsgs    [][]types.Message
	summaryCalls int
}

func (f *compactLLM) Stream(ctx context.Context, req llm.Request) (<-chan llm.Delta, error) {
	out := make(chan llm.Delta, 4)
	f.mu.Lock()
	deltas := f.plan(req)
	f.mu.Unlock()
	go func() {
		defer close(out)
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

// plan 依据请求类型决定本次返回的增量；调用方已持锁。
func (f *compactLLM) plan(req llm.Request) []llm.Delta {
	if isSummaryReq(req) {
		f.summaryCalls++
		return []llm.Delta{{Text: "COMPACT_SUMMARY"}}
	}
	f.reactMsgs = append(f.reactMsgs, append([]types.Message(nil), req.Messages...))
	idx := f.reactIdx
	f.reactIdx++
	if idx == 0 {
		return []llm.Delta{
			{ToolCall: &types.ToolUseBlock{ID: "c1", Name: "read_file", Input: json.RawMessage(`{"path":"a.go"}`)}},
			{Usage: &llm.Usage{PromptTokens: 100}},
		}
	}
	return []llm.Delta{{Text: "done"}}
}

// isSummaryReq 通过系统提示内容判定是否为压缩摘要调用。
func isSummaryReq(req llm.Request) bool {
	return len(req.Messages) > 0 && strings.Contains(req.Messages[0].Text, "compacting")
}

func newCompactEngine(t *testing.T, f *compactLLM, withCtxmgr bool) Engine {
	t.Helper()
	prov, err := observe.New(observe.Config{Enabled: false})
	if err != nil {
		t.Fatalf("observe.New: %v", err)
	}
	deps := Deps{
		LLM:     f,
		Tools:   tool.NewPool(&scriptedTool{name: "read_file", readonly: true, result: types.ToolResult{Content: "body"}}),
		Observe: prov,
		Model:   "t",
	}
	if withCtxmgr {
		deps.Context = contextmgr.New()
	}
	eng, err := New(deps)
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	return eng
}

func TestEngine_CompactTriggered(t *testing.T) {
	// 收紧窗口/保留：used=100 触发压缩，KEEP=1 让切点落在历史中部。
	t.Setenv("COGENT_CONTEXT_WINDOW", "100")
	t.Setenv("COGENT_CONTEXT_RESERVE", "10")
	t.Setenv("COGENT_CONTEXT_BUFFER", "10")
	t.Setenv("COGENT_CONTEXT_KEEP", "1")
	f := &compactLLM{}
	eng := newCompactEngine(t, f, true)

	got := collect(t, mustRun(t, eng, "go"))

	if countType(got, types.EventCompacted) != 1 {
		t.Errorf("EventCompacted count = %d, want 1 (events: %v)", countType(got, types.EventCompacted), eventTypes(got))
	}
	if got[len(got)-1].Type != types.EventDone {
		t.Errorf("last event = %v, want EventDone", eventTypes(got))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.summaryCalls != 1 {
		t.Errorf("summaryCalls = %d, want 1", f.summaryCalls)
	}
	// 压缩后第二轮请求应携带摘要消息，且历史 function calling 配对完整。
	if len(f.reactMsgs) < 2 {
		t.Fatalf("react calls = %d, want >= 2", len(f.reactMsgs))
	}
	assertContainsSummary(t, f.reactMsgs[1])
	assertNoOrphanToolResult(t, f.reactMsgs[1])
}

func TestEngine_NoCompactWhenManagerNil(t *testing.T) {
	f := &compactLLM{}
	eng := newCompactEngine(t, f, false) // 未注入 contextmgr

	got := collect(t, mustRun(t, eng, "go"))

	if countType(got, types.EventCompacted) != 0 {
		t.Errorf("EventCompacted should be 0 without manager, got events %v", eventTypes(got))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.summaryCalls != 0 {
		t.Errorf("summaryCalls = %d, want 0 without manager", f.summaryCalls)
	}
}

// countType 统计某类事件出现次数。
func countType(events []types.StreamEvent, typ types.EventType) int {
	n := 0
	for _, ev := range events {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

// assertContainsSummary 断言消息历史中存在压缩摘要。
func assertContainsSummary(t *testing.T, msgs []types.Message) {
	t.Helper()
	for _, m := range msgs {
		if strings.Contains(m.Text, "COMPACT_SUMMARY") {
			return
		}
	}
	t.Errorf("history missing compaction summary: %+v", msgs)
}

// assertNoOrphanToolResult 断言任一 tool_result 前都紧跟其配对的 assistant(tool_calls)。
func assertNoOrphanToolResult(t *testing.T, msgs []types.Message) {
	t.Helper()
	for i := 1; i < len(msgs); i++ {
		if msgs[i].Role != types.RoleTool {
			continue
		}
		if msgs[i-1].Role != types.RoleAssistant || len(msgs[i-1].ToolCalls) == 0 {
			t.Fatalf("orphan tool_result at %d (preceding %+v)", i, msgs[i-1])
		}
	}
}
