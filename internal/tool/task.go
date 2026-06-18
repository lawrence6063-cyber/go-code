// Package tool 中的 task.go 实现 SubAgent 派发工具：把可隔离的探索子任务交给一个
// 独立上下文的子 Agent 执行，仅把结果摘要回流主循环，避免大量中间消息污染主上下文。
//
// 依赖破环要点（DEV_SPEC §4.4）：派发器需要新建子 Engine，故 agent 包依赖 engine；
// 若让本包反向依赖 agent 将形成环。解决方案是把 Spawner 抽象下沉到 tool 包——task 工具
// 仅依赖 Spawner 接口，agent 包返回的具体实现隐式满足它，无需反向 import。
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/types"
)

// Spawner 派发一个隔离上下文的子 Agent 执行子任务，返回结果摘要。
// 实现位于 internal/agent；接口定义在此以打破 tool↔agent 的循环依赖。
type Spawner interface {
	// Spawn 以独立消息历史与受限工具池执行 prompt 描述的子任务，返回摘要文本。
	Spawn(ctx context.Context, prompt string) (string, error)
}

// taskTool 是 SubAgent 派发工具：只读、放行权限，调用 Spawner 执行隔离子任务并回流摘要。
type taskTool struct {
	Defaults
	spawner Spawner
}

// NewTask 构造 task 工具；spawner 负责实际的隔离派发。
func NewTask(spawner Spawner) Tool { return &taskTool{spawner: spawner} }

// Name 返回工具名。
func (t *taskTool) Name() string { return "task" }

// Description 返回供模型理解用途的描述。
func (t *taskTool) Description() string {
	return "Delegate a self-contained exploration subtask (e.g. locate where a feature is " +
		"implemented in a large repo) to an isolated read-only sub-agent. The sub-agent runs " +
		"with its own context and returns only a concise summary, keeping the main context clean. " +
		"Use it for read-only investigation, not for editing files or running commands."
}

// InputSchema 返回入参 JSON Schema：单个 prompt 字段描述子任务。
func (t *taskTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string","description":"the self-contained subtask for the sub-agent to investigate and report on"}},"required":["prompt"]}`)
}

// IsReadOnly 报告只读：子 Agent 仅装配只读工具池，故派发本身视为只读，plan/ask 档位亦可用。
func (t *taskTool) IsReadOnly(json.RawMessage) bool { return true }

// CheckPermission 派发只读子任务，直接放行（子 Agent 内的高危操作另有其自身权限闸门兜底）。
func (t *taskTool) CheckPermission(context.Context, json.RawMessage) (permission.Decision, error) {
	return permission.Decision{Behavior: permission.BehaviorAllow}, nil
}

// Call 解析子任务描述并派发执行，把摘要作为 tool_result 回流；
// 失败规范化为 IsError 结果（不向上抛 error），让主循环可据此自我修正而不中断。
func (t *taskTool) Call(ctx context.Context, input json.RawMessage, _ ProgressSink) (types.ToolResult, error) {
	var in struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return types.ToolResult{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return types.ToolResult{Content: "empty prompt", IsError: true}, nil
	}
	summary, err := t.spawner.Spawn(ctx, in.Prompt)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("sub-agent failed: %v", err), IsError: true}, nil
	}
	return types.ToolResult{Content: summary}, nil
}
