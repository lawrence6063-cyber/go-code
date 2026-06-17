package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/alaindong/cogent/internal/llm"
	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/session"
	"github.com/alaindong/cogent/internal/tool"
	"github.com/alaindong/cogent/internal/types"
)

// newSessionEngine 构造一个挂上真实 jsonlStore 的引擎，便于验证落盘与 resume。
func newSessionEngine(t *testing.T, f *fakeLLM, store session.Store, sid string, tools ...tool.Tool) Engine {
	t.Helper()
	prov, err := observe.New(observe.Config{Enabled: false})
	if err != nil {
		t.Fatalf("observe.New: %v", err)
	}
	deps := Deps{LLM: f, Observe: prov, Model: "t", Session: store, SessionID: sid}
	if len(tools) > 0 {
		deps.Tools = tool.NewPool(tools...)
	}
	eng, err := New(deps)
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	return eng
}

// TestEngine_RunPersistsEvents 验证一轮带工具的任务把 user/assistant/tool_result 事件落盘。
func TestEngine_RunPersistsEvents(t *testing.T) {
	store := session.NewStore(t.TempDir())
	sid := "run-persist"
	tl := &scriptedTool{name: "read_file", readonly: true, result: types.ToolResult{Content: "body"}}
	f := &fakeLLM{turns: [][]llm.Delta{
		{{ToolCall: &types.ToolUseBlock{ID: "c1", Name: "read_file", Input: json.RawMessage(`{"path":"a"}`)}}},
		{{Text: "all done"}},
	}}
	eng := newSessionEngine(t, f, store, sid, tl)

	collect(t, mustRun(t, eng, "read a"))

	events, err := store.Load(context.Background(), sid)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// 期望事件：user(task) → assistant(tool_call) → tool_result → assistant(final text)。
	wantTypes := []string{"user", "assistant", "tool_result", "assistant"}
	if len(events) != len(wantTypes) {
		t.Fatalf("persisted %d events, want %d: %+v", len(events), len(wantTypes), events)
	}
	for i, wt := range wantTypes {
		if events[i].Type != wt {
			t.Errorf("event[%d].Type = %q, want %q", i, events[i].Type, wt)
		}
	}
	// 事件应经 ParentUUID 串成单链。
	for i := 1; i < len(events); i++ {
		if events[i].ParentUUID != events[i-1].UUID {
			t.Errorf("event[%d].ParentUUID = %q, want %q", i, events[i].ParentUUID, events[i-1].UUID)
		}
	}
}

// TestEngine_ResumeRebuildsAndContinues 验证：先跑一轮落盘，再用新引擎 Resume，
// 重建历史 + 注入 continue + 续跑，且第二段 LLM 请求携带完整重建历史。
func TestEngine_ResumeRebuildsAndContinues(t *testing.T) {
	store := session.NewStore(t.TempDir())
	sid := "resume-continue"

	// 第一段：用户问 → 助手答（无工具），落盘。
	first := &fakeLLM{turns: [][]llm.Delta{{{Text: "first answer"}}}}
	eng1 := newSessionEngine(t, first, store, sid)
	collect(t, mustRun(t, eng1, "first question"))

	// 第二段：新引擎 Resume，应重建历史并注入 continue 后再调一次 LLM。
	second := &fakeLLM{turns: [][]llm.Delta{{{Text: "resumed answer"}}}}
	eng2 := newSessionEngine(t, second, store, sid)
	events, err := eng2.Resume(context.Background(), sid)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	got := collect(t, events)
	if got[len(got)-1].Type != types.EventDone {
		t.Fatalf("resume should end with EventDone, got %v", eventTypes(got))
	}

	second.mu.Lock()
	defer second.mu.Unlock()
	msgs := second.gotMessages[0]
	// 期望：system + user(first question) + assistant(first answer) + user(continue)。
	wantRoles := []types.Role{types.RoleSystem, types.RoleUser, types.RoleAssistant, types.RoleUser}
	if len(msgs) != len(wantRoles) {
		t.Fatalf("resumed messages = %d, want %d: %+v", len(msgs), len(wantRoles), msgs)
	}
	for i, r := range wantRoles {
		if msgs[i].Role != r {
			t.Errorf("msg[%d].Role = %s, want %s", i, msgs[i].Role, r)
		}
	}
	if msgs[1].Text != "first question" || msgs[2].Text != "first answer" {
		t.Errorf("rebuilt history mismatch: %q / %q", msgs[1].Text, msgs[2].Text)
	}
	if msgs[3].Text != continuePrompt {
		t.Errorf("continue prompt = %q, want %q", msgs[3].Text, continuePrompt)
	}
}

// TestEngine_ResumeMissingSession 验证 resume 一个不存在的会话返回错误。
func TestEngine_ResumeMissingSession(t *testing.T) {
	store := session.NewStore(t.TempDir())
	f := &fakeLLM{}
	eng := newSessionEngine(t, f, store, "")
	if _, err := eng.Resume(context.Background(), "ghost"); err == nil {
		t.Error("expected error resuming missing session")
	}
}

// TestFilterUnresolvedToolUses 直测配对修复：剥离无结果的 tool_use、丢弃孤立 tool_result。
func TestFilterUnresolvedToolUses(t *testing.T) {
	tests := []struct {
		name string
		in   []types.Message
		want []types.Message
	}{
		{
			name: "drop unresolved tool_use without result",
			in: []types.Message{
				{Role: types.RoleUser, Text: "q"},
				{Role: types.RoleAssistant, ToolCalls: []types.ToolUseBlock{{ID: "c1", Name: "read"}}},
				// 中断：c1 无对应 tool_result
			},
			want: []types.Message{
				{Role: types.RoleUser, Text: "q"},
				// 助手消息无文本且唯一调用被剥离 → 整条丢弃
			},
		},
		{
			name: "keep resolved, drop orphan result",
			in: []types.Message{
				{Role: types.RoleAssistant, Text: "calling", ToolCalls: []types.ToolUseBlock{{ID: "c1"}, {ID: "c2"}}},
				{Role: types.RoleTool, ToolUseID: "c1", Text: "r1"},
				{Role: types.RoleTool, ToolUseID: "orphan", Text: "stray"},
			},
			want: []types.Message{
				{Role: types.RoleAssistant, Text: "calling", ToolCalls: []types.ToolUseBlock{{ID: "c1"}}},
				{Role: types.RoleTool, ToolUseID: "c1", Text: "r1"},
			},
		},
		{
			name: "fully paired untouched",
			in: []types.Message{
				{Role: types.RoleAssistant, ToolCalls: []types.ToolUseBlock{{ID: "c1"}}},
				{Role: types.RoleTool, ToolUseID: "c1", Text: "r1"},
			},
			want: []types.Message{
				{Role: types.RoleAssistant, ToolCalls: []types.ToolUseBlock{{ID: "c1"}}},
				{Role: types.RoleTool, ToolUseID: "c1", Text: "r1"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterUnresolvedToolUses(tt.in)
			assertMessagesEqual(t, got, tt.want)
		})
	}
}

// assertMessagesEqual 比对消息列表的角色/文本/调用 ID（忽略其它细节）。
func assertMessagesEqual(t *testing.T, got, want []types.Message) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Text != want[i].Text {
			t.Errorf("msg[%d] = {%s,%q}, want {%s,%q}", i, got[i].Role, got[i].Text, want[i].Role, want[i].Text)
		}
		if len(got[i].ToolCalls) != len(want[i].ToolCalls) {
			t.Errorf("msg[%d] tool_calls = %d, want %d", i, len(got[i].ToolCalls), len(want[i].ToolCalls))
			continue
		}
		for j := range want[i].ToolCalls {
			if got[i].ToolCalls[j].ID != want[i].ToolCalls[j].ID {
				t.Errorf("msg[%d].call[%d].ID = %q, want %q", i, j, got[i].ToolCalls[j].ID, want[i].ToolCalls[j].ID)
			}
		}
	}
}
