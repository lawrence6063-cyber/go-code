// Package engine 中的 exec.go 实现工具执行：把模型发起的工具调用经 orchestrate 分批
// （连续只读工具并发、写/执行类串行）后调度执行，回流配对完整的 tool_result。
// 分批与并发调度的核心逻辑在 internal/orchestrate；本文件负责注入 per-tool 执行回调（runOne）。
package engine

import (
	"context"
	"fmt"

	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/orchestrate"
	"github.com/alaindong/cogent/internal/tool"
	"github.com/alaindong/cogent/internal/types"
)

// executeTools 把一批工具调用分批后调度执行，返回每个调用对应的 tool_result 消息（保请求序）。
// 连续只读工具并发执行、写/执行类串行执行；工具失败规范化为 tool_result(IsError) 让模型自我修正；
// ctx 取消则跳过尚未执行的工具（不产出零值结果以保 function calling 配对）。
func (e *engine) executeTools(
	ctx context.Context,
	toolUses []types.ToolUseBlock,
	out chan<- types.StreamEvent,
) []types.Message {
	batches := orchestrate.PartitionBatches(toolUses, e.tools)
	run := func(rctx context.Context, block types.ToolUseBlock) types.Message {
		return e.runOne(rctx, block, out)
	}
	return orchestrate.Run(ctx, batches, run, e.tracer)
}

// runOne 执行单个工具调用：埋 tool.call span，发指标，并把结果规范化为 RoleTool 消息。
func (e *engine) runOne(ctx context.Context, block types.ToolUseBlock, out chan<- types.StreamEvent) types.Message {
	ctx, end := e.tracer.Start(ctx, "tool.call", observe.Attr{Key: "tool.name", Value: block.Name})
	res, err := e.callTool(ctx, block, out)
	end(err)
	e.meter.Count("cogent.tool.calls", 1,
		observe.Attr{Key: "tool.name", Value: block.Name},
		observe.Attr{Key: "is_error", Value: res.IsError || err != nil})
	return toolResultMessage(block, res)
}

// callTool 发 ToolStart/ToolResult 事件并调用工具；未知工具、档位不允许与执行错误
// 均规整为错误结果（不中断主循环）。无论成败都发出成对的工具事件，便于 UI 呈现。
func (e *engine) callTool(
	ctx context.Context,
	block types.ToolUseBlock,
	out chan<- types.StreamEvent,
) (types.ToolResult, error) {
	bl := block
	emitEvent(ctx, out, types.StreamEvent{Type: types.EventToolStart, ToolUse: &bl})

	res, err := e.invokeTool(ctx, block, out)
	if err != nil && ctx.Err() != nil {
		return types.ToolResult{}, err
	}
	if err != nil {
		res = types.ToolResult{Content: fmt.Sprintf("tool error: %v", err), IsError: true}
	}
	r := res
	emitEvent(ctx, out, types.StreamEvent{Type: types.EventToolResult, Result: &r})
	return res, nil
}

// invokeTool 查池、做档位校验并调用工具；未知工具与档位不允许规整为错误结果。
func (e *engine) invokeTool(
	ctx context.Context,
	block types.ToolUseBlock,
	out chan<- types.StreamEvent,
) (types.ToolResult, error) {
	t, ok := e.tools.Get(block.Name)
	if !ok {
		return types.ToolResult{Content: fmt.Sprintf("unknown tool: %s", block.Name), IsError: true}, nil
	}
	if !e.toolAllowedInMode(t) {
		return types.ToolResult{Content: "tool not available in current run mode", IsError: true}, nil
	}
	return t.Call(ctx, block.Input, eventSink{ctx: ctx, out: out})
}

// toolAllowedInMode 报告某工具在当前档位是否可执行：Plan/Ask 仅允许只读工具（fail-closed 二次兜底）。
func (e *engine) toolAllowedInMode(t tool.Tool) bool {
	return e.mode == ModeAuto || t.IsReadOnly(nil)
}

// toolResultMessage 把工具结果规范化为 RoleTool 消息，保持 tool_use↔tool_result 配对完整。
func toolResultMessage(block types.ToolUseBlock, res types.ToolResult) types.Message {
	content := res.Content
	if content == "" {
		if res.IsError {
			content = "error"
		} else {
			content = "ok"
		}
	}
	return types.Message{
		Role:      types.RoleTool,
		Text:      content,
		ToolUseID: block.ID,
		ToolName:  block.Name,
	}
}

// eventSink 把工具执行过程中的进度上报为 EventText 流式事件。
type eventSink struct {
	ctx context.Context
	out chan<- types.StreamEvent
}

// Emit 见 tool.ProgressSink 接口说明。
func (s eventSink) Emit(text string) {
	emitEvent(s.ctx, s.out, types.StreamEvent{Type: types.EventText, Text: text})
}
