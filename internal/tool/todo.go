// Package tool 中的 todo.go 实现 todo_write 工具：让模型维护一份结构化任务清单
// （id/内容/状态），用于长任务过程中显式追踪进度、可视化给用户。这是纯进程内瞬态状态，
// 不落盘、不跨会话持久化——跨会话恢复可后续按需接入 session.Store，本次不在改动范围内。
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/types"
)

// TodoStatus 是任务清单条目的状态。
type TodoStatus string

// 任务状态枚举。
const (
	TodoPending    TodoStatus = "pending"     // 待办
	TodoInProgress TodoStatus = "in_progress" // 进行中
	TodoCompleted  TodoStatus = "completed"   // 已完成
)

// validTodoStatuses 是允许的状态集合，用于入参校验。
var validTodoStatuses = map[TodoStatus]bool{
	TodoPending:    true,
	TodoInProgress: true,
	TodoCompleted:  true,
}

// TodoItem 是任务清单里的一条任务。
type TodoItem struct {
	ID      string     `json:"id"`
	Content string     `json:"content"`
	Status  TodoStatus `json:"status"`
}

// todoTool 持有进程内的当前任务清单；每次 Call 整份覆盖旧状态，互斥锁保护并发读写。
type todoTool struct {
	Defaults
	mu    sync.Mutex
	items []TodoItem
}

// NewTodoWrite 构造 todo_write 工具，初始为空清单。
func NewTodoWrite() Tool { return &todoTool{} }

func (t *todoTool) Name() string { return "todo_write" }
func (t *todoTool) Description() string {
	return "Create or replace the current structured task list to track progress on a " +
		"multi-step task. Each call provides the full list of items (id/content/status); " +
		"it fully replaces the previous list. Use this to make long-running work visible " +
		"and keep track of what is done, in progress, and pending."
}

func (t *todoTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"todos":{"type":"array","description":"the full current task list, replacing any previous list","items":{"type":"object","properties":{"id":{"type":"string"},"content":{"type":"string"},"status":{"type":"string","enum":["pending","in_progress","completed"]}},"required":["id","content","status"]}}},"required":["todos"]}`)
}

// IsReadOnly 报告只读：仅变更进程内瞬态状态，不落盘、不影响工作区文件。
func (t *todoTool) IsReadOnly(json.RawMessage) bool { return true }

// CheckPermission 无外部副作用，直接放行。
func (t *todoTool) CheckPermission(context.Context, json.RawMessage) (permission.Decision, error) {
	return permission.Decision{Behavior: permission.BehaviorAllow}, nil
}

// todoWriteInput 是 todo_write 的入参。
type todoWriteInput struct {
	Todos []TodoItem `json:"todos"`
}

// Call 校验并整份覆盖任务清单，回流格式化后的清单文本。
func (t *todoTool) Call(_ context.Context, input json.RawMessage, _ ProgressSink) (types.ToolResult, error) {
	var in todoWriteInput
	if err := json.Unmarshal(input, &in); err != nil {
		return types.ToolResult{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	if err := validateTodos(in.Todos); err != nil {
		return types.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	t.mu.Lock()
	t.items = in.Todos
	snapshot := make([]TodoItem, len(t.items))
	copy(snapshot, t.items)
	t.mu.Unlock()
	return types.ToolResult{Content: renderTodos(snapshot)}, nil
}

// Snapshot 返回当前任务清单的只读快照，供上层（如状态栏、日志）展示；不属于 Tool 协议，
// 是本工具额外暴露的读取入口，调用方需自行做类型断言取得 *todoTool（或对外暴露的接口）。
func (t *todoTool) Snapshot() []TodoItem {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]TodoItem, len(t.items))
	copy(out, t.items)
	return out
}

// validateTodos 校验每条任务的 id/content 非空、status 合法，且 id 不重复。
func validateTodos(items []TodoItem) error {
	seen := make(map[string]bool, len(items))
	for i, it := range items {
		if it.ID == "" {
			return fmt.Errorf("todos[%d].id must not be empty", i)
		}
		if it.Content == "" {
			return fmt.Errorf("todos[%d].content must not be empty", i)
		}
		if !validTodoStatuses[it.Status] {
			return fmt.Errorf("todos[%d].status %q is invalid (want pending|in_progress|completed)", i, it.Status)
		}
		if seen[it.ID] {
			return fmt.Errorf("duplicate todo id %q", it.ID)
		}
		seen[it.ID] = true
	}
	return nil
}

// renderTodos 把清单格式化为易读文本：[ ] 待办、[~] 进行中、[x] 已完成。
func renderTodos(items []TodoItem) string {
	if len(items) == 0 {
		return "(todo list is empty)"
	}
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "[%s] %s (%s)\n", statusMark(it.Status), it.Content, it.ID)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// statusMark 把状态映射为单字符标记。
func statusMark(s TodoStatus) string {
	switch s {
	case TodoCompleted:
		return "x"
	case TodoInProgress:
		return "~"
	default:
		return " "
	}
}
