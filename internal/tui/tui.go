// Package tui 聚合 cogent 的终端交互层：raw 模式行编辑器（@ 文件 / 斜杠命令补全下拉、
// 历史与反向搜索）、单选菜单、常驻状态栏、成本计量、权限 HITL 交互、类 Claude Code 的
// 流式渲染（工具调用折叠为单行摘要、正文与工具视觉分区），以及交互式 REPL 循环与
// 其 SIGINT 管理。渲染基座与补全/历史分别置于 render/completion/history 子包。
//
// 依赖方向：tui 依赖 engine(接口)/loop/permission/observe 等，这些包均不反向依赖 tui，
// 故无循环依赖。cmd/cogent 只负责运行时装配，通过本包导出的入口函数驱动交互。
package tui

import (
	"context"

	"github.com/alaindong/cogent/internal/engine"
)

// REPLOptions 聚合 REPL 运行选项（由 cmd/cogent 装配后传入）。
type REPLOptions struct {
	First    string // 非空时作为第一轮输入
	ResumeID string // 非空时从该会话恢复
}

// RunDeps 聚合 REPL 运行所需依赖（由 cmd/cogent 装配后注入）。
// Prompter 已在装配阶段织入 engine 的工具权限闸门，故此处无需单列。
type RunDeps struct {
	Engine engine.Engine // 执行内核（接口）
	Input  *InputReader  // 共享 stdin 行来源（REPL 提示与 HITL 复用）
	Bar    *StatusBar    // 常驻状态栏（可为 nil）
}

// RunREPL 进入交互式多轮对话循环：按 opts 决定新建会话或从 ResumeID 恢复。
func RunREPL(ctx context.Context, deps RunDeps, opts REPLOptions) error {
	return driveREPL(ctx, deps.Engine, deps.Input, opts, deps.Bar)
}
