// Package permission 实现工具调用的权限三态裁决（allow/ask/deny）与 Human-in-the-Loop 载体。
// 安全姿态 fail-closed：未显式放行的工具默认走 ask，交由 Prompter 在中断点决策。
package permission

import "encoding/json"

// Behavior 表示一次权限判定的结果。
type Behavior int

// 权限行为枚举。
const (
	BehaviorAsk   Behavior = iota // 需向用户询问
	BehaviorAllow                 // 放行
	BehaviorDeny                  // 拒绝
)

// String 返回行为的可读名，便于日志与 trace 标注。
func (b Behavior) String() string {
	switch b {
	case BehaviorAllow:
		return "allow"
	case BehaviorDeny:
		return "deny"
	default:
		return "ask"
	}
}

// Decision 是一次权限检查的结论。
type Decision struct {
	Behavior     Behavior        // 裁决行为
	UpdatedInput json.RawMessage // 对输入做安全规整后回写（可空）
	Reason       string          // 裁决理由，用于提示与审计
}

// Policy 依据预置规则对一次工具调用做静态裁决，不涉及用户交互；
// 返回 BehaviorAsk 时再交由 Prompter 决定。
type Policy interface {
	// Evaluate 对工具名 + 入参做静态裁决。
	Evaluate(toolName string, input json.RawMessage) Decision
}

// StaticPolicy 依据工具名的 allow/deny 清单做静态裁决：命中 Deny 优先拒绝，
// 命中 Allow 免询问放行，其余返回 Ask（fail-closed）。
type StaticPolicy struct {
	Allow map[string]bool // 免询问放行的工具名集合
	Deny  map[string]bool // 直接拒绝的工具名集合
}

// Evaluate 见 Policy 接口说明。
func (p StaticPolicy) Evaluate(toolName string, _ json.RawMessage) Decision {
	if p.Deny[toolName] {
		return Decision{Behavior: BehaviorDeny, Reason: "tool denied by policy"}
	}
	if p.Allow[toolName] {
		return Decision{Behavior: BehaviorAllow}
	}
	return Decision{Behavior: BehaviorAsk}
}
