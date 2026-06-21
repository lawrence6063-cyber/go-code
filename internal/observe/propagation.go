// Package observe 中的 propagation.go 暴露 W3C Trace Context 注入能力，供跨进程链路串联
// （如 mcp 调用外部 server 时把 traceparent 经环境变量注入子进程，LOOP_SPEC §4.7）。
// 关键约束：OpenTelemetry 的 propagation 类型仅出现在本包内，绝不泄漏到导出 API——
// 调用方（如 mcp）只用标准 map 载体，对 OTel 无感（守 DEV_SPEC §4.4 横切不变量）。
package observe

import (
	"context"

	"go.opentelemetry.io/otel/propagation"
)

// traceContextPropagator 是 W3C Trace Context 传播器（写出 traceparent / tracestate）。
var traceContextPropagator = propagation.TraceContext{}

// InjectTraceContext 把 ctx 中活跃 span 的 W3C trace context（traceparent，必要时 tracestate）
// 写入 carrier（标准 map 载体，键如 "traceparent"）。无活跃/有效 span 时 carrier 保持不变——
// 优雅降级，调用方据此判断是否需要传播（呼应「trace 尽力而为，丢 span 不影响正确性」）。
func InjectTraceContext(ctx context.Context, carrier map[string]string) {
	if carrier == nil {
		return
	}
	traceContextPropagator.Inject(ctx, propagation.MapCarrier(carrier))
}
