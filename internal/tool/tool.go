// Package tool 定义 cogent 的工具运行时协议、工具池与 fail-closed 默认实现。
// 内建工具与（后续）MCP 外部工具同池同协议；新增能力 = 实现接口 + 注册，内核零改动。
package tool

import (
	"context"
	"encoding/json"

	"github.com/alaindong/cogent/internal/llm"
	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/types"
)

// ProgressSink 接收工具执行过程中的进度事件。
type ProgressSink interface {
	// Emit 上报一段进度文本。
	Emit(text string)
}

// Tool 是所有工具的统一运行时协议。默认保守：非并发安全、非只读。
type Tool interface {
	// Name 返回工具名（function calling 标识，须唯一）。
	Name() string
	// Description 返回供模型理解用途的描述。
	Description() string
	// InputSchema 返回入参 JSON Schema，供 function calling 声明。
	InputSchema() json.RawMessage

	// IsConcurrencySafe 报告该输入下工具是否可与其它安全工具并发执行。默认 false。
	IsConcurrencySafe(input json.RawMessage) bool
	// IsReadOnly 报告该输入是否为只读操作。默认 false。
	IsReadOnly(input json.RawMessage) bool

	// CheckPermission 在执行前做权限判定，返回 allow/ask/deny。
	CheckPermission(ctx context.Context, input json.RawMessage) (permission.Decision, error)
	// Call 执行工具；可通过 sink 上报进度。
	Call(ctx context.Context, input json.RawMessage, sink ProgressSink) (types.ToolResult, error)
}

// Defaults 提供 fail-closed 的默认实现（对标蓝本 TOOL_DEFAULTS）：默认非并发安全、
// 非只读、权限为 ask。工具通过匿名嵌入它，只覆写自己需要放开的方法即可——
// 忘记覆写 = 退化为最安全行为，把 fail-closed 从约定变成语言层兜底。
type Defaults struct{}

// IsConcurrencySafe 默认非并发安全。
func (Defaults) IsConcurrencySafe(json.RawMessage) bool { return false }

// IsReadOnly 默认非只读。
func (Defaults) IsReadOnly(json.RawMessage) bool { return false }

// CheckPermission 默认返回 ask，交由上层 Prompter 决策。
func (Defaults) CheckPermission(context.Context, json.RawMessage) (permission.Decision, error) {
	return permission.Decision{Behavior: permission.BehaviorAsk}, nil
}

// Pool 是运行期不可变的工具集合，支持按名查找与 schema 导出。
type Pool interface {
	// Get 按名查找工具。
	Get(name string) (Tool, bool)
	// Schemas 导出全部工具的 function calling 声明。
	Schemas() []llm.ToolSchema
	// All 返回全部工具的快照副本。
	All() []Tool
}

// pool 是 Pool 的切片 + map 实现：启动期装配，运行期只读（无锁、无竞态）。
type pool struct {
	tools []Tool          // 保序工具列表
	index map[string]Tool // 名称索引
}

// NewPool 由一组工具装配只读工具池；同名工具保留先出现者（内建优先于 MCP），nil 忽略。
func NewPool(tools ...Tool) Pool {
	p := &pool{index: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		if t == nil {
			continue
		}
		if _, dup := p.index[t.Name()]; dup {
			continue
		}
		p.index[t.Name()] = t
		p.tools = append(p.tools, t)
	}
	return p
}

// Get 见 Pool 接口说明。
func (p *pool) Get(name string) (Tool, bool) {
	t, ok := p.index[name]
	return t, ok
}

// All 见 Pool 接口说明。
func (p *pool) All() []Tool {
	out := make([]Tool, len(p.tools))
	copy(out, p.tools)
	return out
}

// Schemas 见 Pool 接口说明。
func (p *pool) Schemas() []llm.ToolSchema {
	schemas := make([]llm.ToolSchema, 0, len(p.tools))
	for _, t := range p.tools {
		schemas = append(schemas, llm.ToolSchema{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.InputSchema(),
		})
	}
	return schemas
}
