// Package engine 中的 session_event.go 负责 ReAct 主循环与会话持久化的衔接：
// 把消息记录为 append-only 事件、从事件重建消息列表，并在重建时修复 function calling
// 配对不变量（剥离无结果的 tool_use、丢弃孤立 tool_result）。落盘是增强而非内核正确性前提，
// 持久化失败仅告警不中断主循环（DEV_SPEC §6.5 / §8.5）。
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/alaindong/cogent/internal/session"
	"github.com/alaindong/cogent/internal/types"
)

// continuePrompt 在 resume 时注入，提示模型从中断处接续未完成的任务。
const continuePrompt = "Continue from where you left off."

// record 把一条消息持久化为会话事件；未配置 Session 时直接跳过（向后兼容）。
// 事件经 ParentUUID 串成链，落盘失败仅告警（transcript 是增强而非内核正确性前提）。
func (e *engine) record(ctx context.Context, msg types.Message) {
	if e.session == nil || e.sessionID == "" {
		return
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		slog.Warn("marshal session event", "err", err)
		return
	}
	ev := session.Event{
		UUID:       genUUID(),
		ParentUUID: e.lastUUID,
		Type:       eventType(msg.Role),
		Payload:    payload,
		Timestamp:  time.Now().UnixNano(),
	}
	if err := e.session.Append(ctx, e.sessionID, ev); err != nil {
		slog.Warn("append session event", "err", err)
		return
	}
	e.lastUUID = ev.UUID
}

// eventType 把消息角色映射为事件类型字符串（对齐 §5.9 Event.Type 枚举）。
func eventType(role types.Role) string {
	switch role {
	case types.RoleUser:
		return "user"
	case types.RoleAssistant:
		return "assistant"
	case types.RoleTool:
		return "tool_result"
	default:
		return "meta"
	}
}

// rebuildMessages 把已加载的事件列表重建为消息列表（跳过 system，由内核重新注入；
// summary/meta 等非消息事件忽略）。遇到 undo 事件时回退到其标记的截断点。
// 返回的消息尚未做配对修复。
func rebuildMessages(events []session.Event) ([]types.Message, string) {
	type msgEntry struct {
		msg  types.Message
		uuid string
	}
	entries := make([]msgEntry, 0, len(events))
	uuidIndex := make(map[string]int) // UUID → entries 中该事件之后的位置
	lastUUID := ""

	for _, ev := range events {
		lastUUID = ev.UUID
		if ev.Type == "undo" {
			var payload undoPayload
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				slog.Warn("unmarshal undo event", "uuid", ev.UUID, "err", err)
				continue
			}
			if len(payload.RevokedUUIDs) > 0 {
				cutUUID := payload.RevokedUUIDs[0]
				if idx, ok := uuidIndex[cutUUID]; ok {
					entries = entries[:idx]
				}
			}
			continue
		}
		if ev.Type != "user" && ev.Type != "assistant" && ev.Type != "tool_result" {
			continue
		}
		var msg types.Message
		if err := json.Unmarshal(ev.Payload, &msg); err != nil {
			slog.Warn("unmarshal session event", "uuid", ev.UUID, "err", err)
			continue
		}
		if msg.Role == types.RoleSystem {
			continue
		}
		uuidIndex[ev.UUID] = len(entries) + 1
		entries = append(entries, msgEntry{msg: msg, uuid: ev.UUID})
	}

	msgs := make([]types.Message, 0, len(entries))
	for _, e := range entries {
		msgs = append(msgs, e.msg)
	}
	return msgs, lastUUID
}

// filterUnresolvedToolUses 修复 function calling 配对不变量（恢复路径核心，DEV_SPEC §6.5）：
// 中断可能留下"已请求但无结果"的 tool_use 或"无对应调用"的孤立 tool_result，
// 直接交回 LLM 会触发 API 报错。算法：先收集有结果的 ToolUseID，再剥离无结果的 tool_use，
// 最后丢弃指向已被剥离调用的孤立 tool_result。
func filterUnresolvedToolUses(msgs []types.Message) []types.Message {
	resolved := collectResolvedIDs(msgs)
	kept := make(map[string]bool)
	out := make([]types.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == types.RoleAssistant && len(m.ToolCalls) > 0 {
			m.ToolCalls = keepResolvedCalls(m.ToolCalls, resolved, kept)
			if m.Text == "" && len(m.ToolCalls) == 0 {
				continue // 助手消息既无文本又无有效调用，整条丢弃
			}
		}
		if m.Role == types.RoleTool && !kept[m.ToolUseID] {
			continue // 孤立 tool_result：对应调用已不存在
		}
		out = append(out, m)
	}
	return out
}

// collectResolvedIDs 收集所有存在 tool_result 的 ToolUseID 集合。
func collectResolvedIDs(msgs []types.Message) map[string]bool {
	resolved := make(map[string]bool)
	for _, m := range msgs {
		if m.Role == types.RoleTool && m.ToolUseID != "" {
			resolved[m.ToolUseID] = true
		}
	}
	return resolved
}

// keepResolvedCalls 仅保留有对应结果的工具调用，并把被保留的 ID 记入 kept 供后续过滤孤立结果。
func keepResolvedCalls(calls []types.ToolUseBlock, resolved, kept map[string]bool) []types.ToolUseBlock {
	out := make([]types.ToolUseBlock, 0, len(calls))
	for _, c := range calls {
		if resolved[c.ID] {
			kept[c.ID] = true
			out = append(out, c)
		}
	}
	return out
}

// genUUID 用标准库 crypto/rand 生成 16 字节 hex 作为事件 UUID（不引入第三方依赖）。
func genUUID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(buf[:])
}
