// Package types 定义 cogent 各包共享的最内层类型，不依赖任何业务包，杜绝循环依赖。
package types

import "encoding/json"

// Role 表示一条消息的角色。
type Role string

// 消息角色枚举。
const (
	RoleSystem    Role = "system"    // 系统提示
	RoleUser      Role = "user"      // 用户输入
	RoleAssistant Role = "assistant" // 模型回复
	RoleTool      Role = "tool"      // 工具结果
)

// Message 是上下文中的一条消息，可携带文本、工具调用或工具结果。
type Message struct {
	Role      Role           // 消息角色
	Text      string         // 文本内容
	ToolCalls []ToolUseBlock // assistant 发起的工具调用
	ToolUseID string         // tool 角色：对应的调用 ID
	ToolName  string         // tool 角色：对应的工具名
}

// ToolUseBlock 表示模型一次 function calling 请求。
type ToolUseBlock struct {
	ID    string          // 调用 ID，用于与 tool_result 配对
	Name  string          // 工具名
	Input json.RawMessage // 工具入参（原始 JSON）
}

// ToolResult 是工具执行结果，将被规范化为 tool_result 回流给模型。
// 定义在 types 包以避免上层引入业务包依赖（见架构依赖方向约束）。
type ToolResult struct {
	Content string // 结果文本
	IsError bool   // 是否为错误结果
}

// EventType 标识一个流式事件的类型。
type EventType int

// 流式事件类型枚举。
const (
	EventText       EventType = iota // 助手文本增量
	EventToolStart                   // 工具开始执行
	EventToolResult                  // 工具结果
	EventCompacted                   // 发生了上下文压缩
	EventDone                        // 任务结束
	EventError                       // 发生错误
)

// StreamEvent 是执行内核向上游流式输出的统一事件。
type StreamEvent struct {
	Type    EventType     // 事件类型
	Text    string        // 文本增量（EventText 时有效）
	ToolUse *ToolUseBlock // 工具调用（EventToolStart 时有效）
	Result  *ToolResult   // 工具结果（EventToolResult 时有效）
	Err     error         // 错误（EventError 时有效）
}
