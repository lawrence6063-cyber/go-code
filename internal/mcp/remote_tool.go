package mcp

import (
	"context"
	"encoding/json"

	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/tool"
	"github.com/alaindong/cogent/internal/types"
)

// emptyObjectSchema 是远端工具缺省的入参 schema（无声明时回退为开放对象）。
var emptyObjectSchema = json.RawMessage(`{"type":"object"}`)

// ToolName 把 server 名与原始工具名拼为内核唯一标识 mcp__<server>__<tool>。
// 该前缀实现命名空间隔离：外部工具无法冒用内建工具名（融合时内建优先去重）。
func ToolName(server, name string) string {
	return "mcp__" + server + "__" + name
}

// remoteTool 把一个远端 MCP 工具适配为 cogent 的 tool.Tool。
// 匿名嵌入 tool.Defaults：外部工具默认非并发安全、非只读、权限 ask（fail-closed），
// 须经 Guard 包裹后入池，统一过 permission/HITL。
type remoteTool struct {
	tool.Defaults
	client   *client
	origName string
	fullName string
	desc     string
	schema   json.RawMessage
}

// newRemoteTool 依据 server 工具声明构造远端工具。
func newRemoteTool(c *client, spec toolSpec) *remoteTool {
	schema := spec.InputSchema
	if len(schema) == 0 {
		schema = emptyObjectSchema
	}
	return &remoteTool{
		client:   c,
		origName: spec.Name,
		fullName: ToolName(c.name, spec.Name),
		desc:     spec.Description,
		schema:   schema,
	}
}

// Name 返回带前缀的唯一工具名。
func (t *remoteTool) Name() string { return t.fullName }

// Description 返回远端工具描述。
func (t *remoteTool) Description() string { return t.desc }

// InputSchema 返回远端工具入参 schema。
func (t *remoteTool) InputSchema() json.RawMessage { return t.schema }

// Call 经 mcp.call span 调用远端工具；传输/解码错误规范化为错误结果回流（不向上抛 error）。
func (t *remoteTool) Call(
	ctx context.Context,
	input json.RawMessage,
	_ tool.ProgressSink,
) (types.ToolResult, error) {
	ctx, end := t.client.tracer.Start(ctx, "mcp.call",
		observe.Attr{Key: "mcp.server", Value: t.client.name},
		observe.Attr{Key: "mcp.tool", Value: t.origName},
	)
	res, err := t.client.callTool(ctx, t.origName, input)
	end(err)
	if err != nil {
		return types.ToolResult{Content: "mcp call failed: " + err.Error(), IsError: true}, nil
	}
	return res, nil
}
