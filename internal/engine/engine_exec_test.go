package engine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/alaindong/cogent/internal/llm"
	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/tool"
	"github.com/alaindong/cogent/internal/types"
)

// scriptedTool 是 tool.Tool 的测试替身，记录调用次数并返回预置结果。
type scriptedTool struct {
	tool.Defaults
	name     string
	readonly bool
	result   types.ToolResult
	calls    int
}

func (s *scriptedTool) Name() string                           { return s.name }
func (s *scriptedTool) Description() string                    { return "scripted tool" }
func (s *scriptedTool) InputSchema() json.RawMessage           { return json.RawMessage(`{"type":"object"}`) }
func (s *scriptedTool) IsReadOnly(json.RawMessage) bool        { return s.readonly }
func (s *scriptedTool) IsConcurrencySafe(json.RawMessage) bool { return s.readonly }

func (s *scriptedTool) CheckPermission(context.Context, json.RawMessage) (permission.Decision, error) {
	return permission.Decision{Behavior: permission.BehaviorAllow}, nil
}

func (s *scriptedTool) Call(context.Context, json.RawMessage, tool.ProgressSink) (types.ToolResult, error) {
	s.calls++
	return s.result, nil
}

func newToolEngine(t *testing.T, f *fakeLLM, mode Mode, maxSteps int, tools ...tool.Tool) Engine {
	t.Helper()
	prov, err := observe.New(observe.Config{Enabled: false})
	if err != nil {
		t.Fatalf("observe.New: %v", err)
	}
	eng, err := New(Deps{LLM: f, Tools: tool.NewPool(tools...), Observe: prov, Model: "t", Mode: mode, MaxSteps: maxSteps})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	return eng
}

func eventTypes(events []types.StreamEvent) []types.EventType {
	out := make([]types.EventType, len(events))
	for i, ev := range events {
		out[i] = ev.Type
	}
	return out
}

func TestEngine_SingleToolLoop(t *testing.T) {
	tl := &scriptedTool{name: "read_file", readonly: true, result: types.ToolResult{Content: "file body"}}
	f := &fakeLLM{turns: [][]llm.Delta{
		{{ToolCall: &types.ToolUseBlock{ID: "c1", Name: "read_file", Input: json.RawMessage(`{"path":"a.go"}`)}}},
		{{Text: "done reading"}},
	}}
	eng := newToolEngine(t, f, ModeAuto, 0, tl)

	got := collect(t, mustRun(t, eng, "read a.go"))

	if tl.calls != 1 {
		t.Errorf("tool calls = %d, want 1", tl.calls)
	}
	// 事件序列应含 ToolStart → ToolResult → Text → Done。
	want := []types.EventType{types.EventToolStart, types.EventToolResult, types.EventText, types.EventDone}
	if diff := compareTypes(eventTypes(got), want); diff != "" {
		t.Errorf("event sequence mismatch: %s (got %v)", diff, eventTypes(got))
	}
	// 第二轮请求应携带配对完整的 assistant(tool_calls) + tool(result) 历史。
	f.mu.Lock()
	defer f.mu.Unlock()
	second := f.gotMessages[1]
	assertToolPairing(t, second)
}

// compareTypes 比对事件类型序列，返回首个差异描述（空表示一致）。
func compareTypes(got, want []types.EventType) string {
	if len(got) != len(want) {
		return "length mismatch"
	}
	for i := range want {
		if got[i] != want[i] {
			return "index difference"
		}
	}
	return ""
}

// assertToolPairing 断言历史中存在带 tool_calls 的 assistant 消息及其配对的 tool 消息。
func assertToolPairing(t *testing.T, msgs []types.Message) {
	t.Helper()
	var assistant, toolMsg *types.Message
	for i := range msgs {
		switch msgs[i].Role {
		case types.RoleAssistant:
			if len(msgs[i].ToolCalls) > 0 {
				assistant = &msgs[i]
			}
		case types.RoleTool:
			toolMsg = &msgs[i]
		}
	}
	if assistant == nil {
		t.Fatal("no assistant message with tool_calls in history")
	}
	if toolMsg == nil {
		t.Fatal("no tool result message in history")
	}
	if toolMsg.ToolUseID != assistant.ToolCalls[0].ID {
		t.Errorf("tool result ID %q != tool_use ID %q (pairing broken)", toolMsg.ToolUseID, assistant.ToolCalls[0].ID)
	}
}

func TestEngine_MaxStepsGuard(t *testing.T) {
	tl := &scriptedTool{name: "loop", readonly: true, result: types.ToolResult{Content: "x"}}
	loopTurn := []llm.Delta{{ToolCall: &types.ToolUseBlock{ID: "c", Name: "loop", Input: json.RawMessage(`{}`)}}}
	f := &fakeLLM{turns: [][]llm.Delta{loopTurn, loopTurn, loopTurn}}
	eng := newToolEngine(t, f, ModeAuto, 3, tl)

	got := collect(t, mustRun(t, eng, "loop forever"))

	last := got[len(got)-1]
	if last.Type != types.EventError || !errors.Is(last.Err, ErrMaxStepsExceeded) {
		t.Errorf("last event = %+v, want EventError(ErrMaxStepsExceeded)", last)
	}
}

func TestEngine_PlanModeExposesOnlyReadOnlyTools(t *testing.T) {
	ro := &scriptedTool{name: "read_file", readonly: true}
	wr := &scriptedTool{name: "write_file", readonly: false}
	f := &fakeLLM{turns: [][]llm.Delta{{{Text: "here is the plan"}}}}
	eng := newToolEngine(t, f, ModePlan, 0, ro, wr)

	collect(t, mustRun(t, eng, "make a plan"))

	f.mu.Lock()
	defer f.mu.Unlock()
	tools := f.gotTools[0]
	if len(tools) != 1 || tools[0].Name != "read_file" {
		t.Errorf("plan mode tools = %v, want only read_file", tools)
	}
}

func TestEngine_UnknownToolNormalized(t *testing.T) {
	f := &fakeLLM{turns: [][]llm.Delta{
		{{ToolCall: &types.ToolUseBlock{ID: "c1", Name: "ghost", Input: json.RawMessage(`{}`)}}},
		{{Text: "recovered"}},
	}}
	eng := newToolEngine(t, f, ModeAuto, 0) // 空工具池

	got := collect(t, mustRun(t, eng, "call ghost"))

	var result *types.ToolResult
	for i := range got {
		if got[i].Type == types.EventToolResult {
			result = got[i].Result
		}
	}
	if result == nil || !result.IsError {
		t.Errorf("unknown tool should yield error tool_result, got %+v", result)
	}
	if got[len(got)-1].Type != types.EventDone {
		t.Errorf("should recover to EventDone, got %v", eventTypes(got))
	}
}
