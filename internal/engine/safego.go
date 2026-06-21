// Package engine 中的 safego.go 提供 goroutine 边界的 panic 兜底：
// 把单点 panic 降级为该任务的错误，绝不让其逃逸到 runtime 终止整个进程。
// 这是无人值守长跑的可靠性底线（OPTIMIZE_SPEC R2）。
//
// 架构取舍（OPTIMIZE_SPEC A3）：内核各 goroutine（engine.Run/Resume、llm.pump、orchestrate 并发批、
// agent.Spawn）均直接 go func 启动、无统一 worker 池。这是刻意的轻量取舍——生命周期由「ctx 取消 +
// channel close 责任明确」管理已足够清晰（goleak 验证无泄漏），引入 goroutine 池属过度设计。
// safeGo 只负责顶层 panic 兜底，与是否池化正交。
package engine

// safeGo 在 fn 执行的 goroutine 顶层兜底 panic：发生 panic 时调用 onPanic 回调（由上层转为
// EventError / ToolResult），而非让进程崩溃。recover 仅用于此处顶层兜底，不替代正常 error 流。
// 注意：recover 返回 interface{}，不假设其为 error、不命名为 err（遵循编码规范）。
func safeGo(onPanic func(v any), fn func()) {
	defer func() {
		if v := recover(); v != nil {
			onPanic(v)
		}
	}()
	fn()
}
