// Package engine 中的 undo.go 实现 REPL 一键回退（Undo）功能：
// 从 turnSnapshots 栈弹出最近一轮快照，截断消息历史并恢复工作区文件状态。
package engine

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/alaindong/cogent/internal/session"
	"github.com/alaindong/cogent/internal/types"
)

// undoPayload 是 undo 事件的 JSON payload，记录被撤销消息对应的事件 UUID 列表。
type undoPayload struct {
	RevokedUUIDs []string `json:"revoked_uuids"`
}

// Undo 撤销最近一轮对话：回退消息历史并恢复工作区快照（若有）。
// 无可撤销轮次时返回 ErrNothingToUndo。
func (e *engine) Undo(ctx context.Context) (*UndoResult, error) {
	if len(e.turnSnapshots) == 0 {
		return nil, ErrNothingToUndo
	}

	// 弹出最后一个快照
	snap := e.turnSnapshots[len(e.turnSnapshots)-1]
	e.turnSnapshots = e.turnSnapshots[:len(e.turnSnapshots)-1]

	// 计算被移除的消息
	removed := e.msgs[snap.msgIndex:]
	removedCount := len(removed)

	// 生成轮次摘要：取被移除的第一条 user 消息的前 50 字符
	summary := buildUndoSummary(removed)

	// 截断消息历史
	e.msgs = e.msgs[:snap.msgIndex]

	// 恢复 session 事件链的 lastUUID 到快照记录的位置
	e.lastUUID = snap.lastUUID

	// 尝试恢复工作区文件
	hasFileChanges := false
	if e.snapshotter != nil && e.snapshotter.IsGitRepo() {
		if err := e.snapshotter.Restore(ctx, snap.stashID); err != nil {
			slog.Warn("undo: workspace restore failed", "err", err)
			// 恢复失败仅告警，不阻断消息回退（解耦）
		} else {
			hasFileChanges = true
		}
	}

	// 记录 undo 事件到 session transcript
	e.recordUndoEvent(ctx, snap)

	return &UndoResult{
		Summary:        summary,
		HasFileChanges: hasFileChanges,
		RemovedCount:   removedCount,
	}, nil
}

// recordUndoEvent 向 session transcript 追加一条 undo 事件，payload 中包含被撤销的事件 UUID 信息。
func (e *engine) recordUndoEvent(ctx context.Context, snap turnSnapshot) {
	if e.session == nil || e.sessionID == "" {
		return
	}
	payload, err := json.Marshal(undoPayload{
		RevokedUUIDs: []string{snap.lastUUID},
	})
	if err != nil {
		slog.Warn("marshal undo event", "err", err)
		return
	}
	ev := session.Event{
		UUID:       genUUID(),
		ParentUUID: e.lastUUID,
		Type:       "undo",
		Payload:    payload,
		Timestamp:  time.Now().UnixNano(),
	}
	if err := e.session.Append(ctx, e.sessionID, ev); err != nil {
		slog.Warn("append undo event", "err", err)
		return
	}
	e.lastUUID = ev.UUID
}

// buildUndoSummary 从被移除的消息中提取摘要：取第一条 user 消息的前 50 字符。
func buildUndoSummary(msgs []types.Message) string {
	for _, m := range msgs {
		if m.Role == types.RoleUser {
			if len(m.Text) > 50 {
				return m.Text[:50] + "..."
			}
			return m.Text
		}
	}
	return ""
}

// takeTurnSnapshot 在每轮对话开始前记录当前引擎状态快照，压入 turnSnapshots 栈。
// 包含消息切片长度、工作区 git 快照 ID 和 session 事件链尾 UUID。
func (e *engine) takeTurnSnapshot(ctx context.Context) {
	snap := turnSnapshot{
		msgIndex: len(e.msgs),
		lastUUID: e.lastUUID,
	}
	if e.snapshotter != nil && e.snapshotter.IsGitRepo() {
		id, err := e.snapshotter.Take(ctx)
		if err != nil {
			slog.Warn("take workspace snapshot failed", "err", err)
		} else {
			snap.stashID = id
		}
	}
	e.turnSnapshots = append(e.turnSnapshots, snap)
}
