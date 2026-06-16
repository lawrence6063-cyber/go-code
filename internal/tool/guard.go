// Package tool 中的 guard.go 实现权限执行装饰器：把权限判定 + 人在环（HITL）收敛到一处。
package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/types"
)

// Guard 给工具加权限闸门：执行前做 permission.check 埋点 + 三态裁决 + HITL 中断介入。
// 写/执行类工具应被 Guard 包裹后入池；只读类可直接入池。Guard 自身实现 Tool，元数据透传内层。
type Guard struct {
	inner    Tool                // 被包裹的真实工具
	policy   permission.Policy   // 静态裁决策略（可空）
	prompter permission.Prompter // 中断决策器（可空，空则 ask 一律 fail-closed 拒绝）
	tracer   observe.Tracer      // 埋点
}

// NewGuard 用权限策略、中断决策器与 tracer 包裹一个工具。
func NewGuard(inner Tool, policy permission.Policy, prompter permission.Prompter, tracer observe.Tracer) *Guard {
	return &Guard{inner: inner, policy: policy, prompter: prompter, tracer: tracer}
}

// Name 透传内层工具名。
func (g *Guard) Name() string { return g.inner.Name() }

// Description 透传内层描述。
func (g *Guard) Description() string { return g.inner.Description() }

// InputSchema 透传内层入参 schema。
func (g *Guard) InputSchema() json.RawMessage { return g.inner.InputSchema() }

// IsConcurrencySafe 透传内层并发安全性。
func (g *Guard) IsConcurrencySafe(input json.RawMessage) bool {
	return g.inner.IsConcurrencySafe(input)
}

// IsReadOnly 透传内层只读性。
func (g *Guard) IsReadOnly(input json.RawMessage) bool { return g.inner.IsReadOnly(input) }

// CheckPermission 透传内层权限判定。
func (g *Guard) CheckPermission(ctx context.Context, input json.RawMessage) (permission.Decision, error) {
	return g.inner.CheckPermission(ctx, input)
}

// Call 在权限闸门内执行内层工具：allow 放行、deny 回错误结果、ask 走 HITL。
func (g *Guard) Call(ctx context.Context, input json.RawMessage, sink ProgressSink) (types.ToolResult, error) {
	dec := g.decide(ctx, input)
	switch dec.Behavior {
	case permission.BehaviorAllow:
		return g.inner.Call(ctx, pickInput(input, dec.UpdatedInput), sink)
	case permission.BehaviorDeny:
		return types.ToolResult{Content: denyMessage(dec.Reason), IsError: true}, nil
	default:
		return g.askAndRun(ctx, input, dec, sink)
	}
}

// decide 在 permission.check span 内综合静态策略与工具内建判定得出裁决。
func (g *Guard) decide(ctx context.Context, input json.RawMessage) permission.Decision {
	_, end := g.tracer.Start(ctx, "permission.check", observe.Attr{Key: "tool.name", Value: g.inner.Name()})
	dec := g.resolve(ctx, input)
	end(nil)
	return dec
}

// resolve 计算静态裁决：策略命中 allow/deny 即采纳，否则回落到工具内建 CheckPermission。
func (g *Guard) resolve(ctx context.Context, input json.RawMessage) permission.Decision {
	if g.policy != nil {
		if d := g.policy.Evaluate(g.inner.Name(), input); d.Behavior != permission.BehaviorAsk {
			return d
		}
	}
	d, err := g.inner.CheckPermission(ctx, input)
	if err != nil {
		return permission.Decision{Behavior: permission.BehaviorDeny, Reason: err.Error()}
	}
	return d
}

// askAndRun 在中断点征询人类决策并据此执行（Approve/Edit）或拒绝（Reject）。
func (g *Guard) askAndRun(
	ctx context.Context,
	input json.RawMessage,
	dec permission.Decision,
	sink ProgressSink,
) (types.ToolResult, error) {
	if g.prompter == nil {
		return types.ToolResult{Content: "permission required but no prompter configured", IsError: true}, nil
	}
	res, err := g.prompter.Ask(ctx, permission.Interrupt{Tool: g.inner.Name(), Input: input, Reason: dec.Reason})
	if err != nil {
		return types.ToolResult{}, fmt.Errorf("prompter ask: %w", err)
	}
	switch res.Action {
	case permission.ActionApprove:
		return g.inner.Call(ctx, input, sink)
	case permission.ActionEdit:
		return g.inner.Call(ctx, pickInput(input, res.UpdatedInput), sink)
	default:
		return types.ToolResult{Content: rejectMessage(res.Guidance), IsError: true}, nil
	}
}

// pickInput 在有修正入参时采用修正值，否则用原始入参。
func pickInput(orig, updated json.RawMessage) json.RawMessage {
	if len(updated) > 0 {
		return updated
	}
	return orig
}

// denyMessage 构造拒绝执行的回流文本（供模型理解为何被拒）。
func denyMessage(reason string) string {
	if reason == "" {
		reason = "denied by permission policy"
	}
	return "tool execution denied: " + reason
}

// rejectMessage 构造人类拒绝时回流给模型的指引文本。
func rejectMessage(guidance string) string {
	if guidance == "" {
		return "tool execution rejected by user"
	}
	return "tool execution rejected by user; guidance: " + guidance
}
