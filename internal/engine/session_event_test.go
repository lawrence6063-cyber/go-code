package engine

import (
	"encoding/json"
	"testing"

	"github.com/alaindong/cogent/internal/session"
	"github.com/alaindong/cogent/internal/types"
)

func TestRebuildMessages_Basic(t *testing.T) {
	// 基本重建：user + assistant 事件正确重建为消息列表
	events := []session.Event{
		makeEvent("u1", "", "user", types.Message{Role: types.RoleUser, Text: "hello"}),
		makeEvent("a1", "u1", "assistant", types.Message{Role: types.RoleAssistant, Text: "hi"}),
	}
	msgs, lastUUID := rebuildMessages(events)
	if len(msgs) != 2 {
		t.Fatalf("msgs len = %d, want 2", len(msgs))
	}
	if msgs[0].Text != "hello" || msgs[1].Text != "hi" {
		t.Errorf("msgs = %+v", msgs)
	}
	if lastUUID != "a1" {
		t.Errorf("lastUUID = %q, want %q", lastUUID, "a1")
	}
}

func TestRebuildMessages_WithUndo(t *testing.T) {
	// undo 事件应导致被撤销的消息从重建结果中排除
	events := []session.Event{
		makeEvent("u1", "", "user", types.Message{Role: types.RoleUser, Text: "q1"}),
		makeEvent("a1", "u1", "assistant", types.Message{Role: types.RoleAssistant, Text: "r1"}),
		makeEvent("u2", "a1", "user", types.Message{Role: types.RoleUser, Text: "q2"}),
		makeEvent("a2", "u2", "assistant", types.Message{Role: types.RoleAssistant, Text: "r2"}),
		// undo 事件：撤销 u2 和 a2（截断点为 a1，即 a1 之后的消息被撤销）
		makeUndoEvent("undo1", "a2", "a1"),
	}
	msgs, lastUUID := rebuildMessages(events)
	if len(msgs) != 2 {
		t.Fatalf("msgs len = %d, want 2", len(msgs))
	}
	if msgs[0].Text != "q1" || msgs[1].Text != "r1" {
		t.Errorf("msgs = %+v", msgs)
	}
	if lastUUID != "undo1" {
		t.Errorf("lastUUID = %q, want %q", lastUUID, "undo1")
	}
}

func TestRebuildMessages_UndoThenNewTurn(t *testing.T) {
	// undo 后又有新轮次，新轮次应保留
	events := []session.Event{
		makeEvent("u1", "", "user", types.Message{Role: types.RoleUser, Text: "q1"}),
		makeEvent("a1", "u1", "assistant", types.Message{Role: types.RoleAssistant, Text: "r1"}),
		makeEvent("u2", "a1", "user", types.Message{Role: types.RoleUser, Text: "q2"}),
		makeEvent("a2", "u2", "assistant", types.Message{Role: types.RoleAssistant, Text: "r2"}),
		makeUndoEvent("undo1", "a2", "a1"),
		// undo 后的新轮次
		makeEvent("u3", "undo1", "user", types.Message{Role: types.RoleUser, Text: "q3"}),
		makeEvent("a3", "u3", "assistant", types.Message{Role: types.RoleAssistant, Text: "r3"}),
	}
	msgs, _ := rebuildMessages(events)
	if len(msgs) != 4 {
		t.Fatalf("msgs len = %d, want 4", len(msgs))
	}
	wantTexts := []string{"q1", "r1", "q3", "r3"}
	for i, want := range wantTexts {
		if msgs[i].Text != want {
			t.Errorf("msgs[%d].Text = %q, want %q", i, msgs[i].Text, want)
		}
	}
}

func TestRebuildMessages_ConsecutiveUndo(t *testing.T) {
	// 连续两次 undo
	events := []session.Event{
		makeEvent("u1", "", "user", types.Message{Role: types.RoleUser, Text: "q1"}),
		makeEvent("a1", "u1", "assistant", types.Message{Role: types.RoleAssistant, Text: "r1"}),
		makeEvent("u2", "a1", "user", types.Message{Role: types.RoleUser, Text: "q2"}),
		makeEvent("a2", "u2", "assistant", types.Message{Role: types.RoleAssistant, Text: "r2"}),
		makeEvent("u3", "a2", "user", types.Message{Role: types.RoleUser, Text: "q3"}),
		makeEvent("a3", "u3", "assistant", types.Message{Role: types.RoleAssistant, Text: "r3"}),
		// 第一次 undo：撤销 q3/r3
		makeUndoEvent("undo1", "a3", "a2"),
		// 第二次 undo：撤销 q2/r2
		makeUndoEvent("undo2", "undo1", "a1"),
	}
	msgs, _ := rebuildMessages(events)
	if len(msgs) != 2 {
		t.Fatalf("msgs len = %d, want 2", len(msgs))
	}
	if msgs[0].Text != "q1" || msgs[1].Text != "r1" {
		t.Errorf("msgs = %+v", msgs)
	}
}

// makeEvent 构造一个测试用的 session.Event。
func makeEvent(uuid, parent, typ string, msg types.Message) session.Event {
	payload, _ := json.Marshal(msg)
	return session.Event{
		UUID:       uuid,
		ParentUUID: parent,
		Type:       typ,
		Payload:    payload,
		Timestamp:  1,
	}
}

// makeUndoEvent 构造一个 undo 类型的 session.Event。
func makeUndoEvent(uuid, parent, cutUUID string) session.Event {
	payload, _ := json.Marshal(undoPayload{RevokedUUIDs: []string{cutUUID}})
	return session.Event{
		UUID:       uuid,
		ParentUUID: parent,
		Type:       "undo",
		Payload:    payload,
		Timestamp:  1,
	}
}
