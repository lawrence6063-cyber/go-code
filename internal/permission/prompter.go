// Package permission 中的 prompter.go 定义 Human-in-the-Loop 的中断决策协议。
package permission

import (
	"context"
	"encoding/json"
)

// Prompter 在中断点（interrupt）向人类请求决策，是 Human-in-the-Loop 的载体：
// CLI 实现读 stdin 交互；Headless 实现走预设保守策略。
type Prompter interface {
	// Ask 就一次中断点征询处置；ctx 取消即应尽快返回。
	Ask(ctx context.Context, req Interrupt) (Resolution, error)
}

// Interrupt 描述一次需人类介入的中断点。
type Interrupt struct {
	Tool   string          // 待执行的工具名
	Input  json.RawMessage // 待执行的入参，供人类审阅
	Reason string          // 为何需要介入（命中 ask / 危险操作等）
}

// Action 表示人类在中断点的处置动作。
type Action int

// 中断处置动作枚举。
const (
	ActionApprove Action = iota // 批准，原样执行
	ActionEdit                  // 修改入参后批准
	ActionReject                // 拒绝，并可附 Guidance 指引模型改道
)

// Resolution 是人类对一个中断点的处置结果。
type Resolution struct {
	Action       Action          // 处置动作
	UpdatedInput json.RawMessage // Action=ActionEdit 时回写的修正入参
	Guidance     string          // 拒绝/编辑时给模型的自然语言指引（回流为 tool_result）
}
