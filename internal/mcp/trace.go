package mcp

import (
	"context"

	"github.com/alaindong/cogent/internal/observe"
)

// W3C Trace Context 跨进程传播的环境变量键（OTel 约定的大写形式）。
const (
	envTraceparent = "TRACEPARENT" // W3C traceparent
	envTracestate  = "TRACESTATE"  // W3C tracestate（可选）
)

// InjectTraceContext 把当前 span 的 W3C trace context 经环境变量注入待启动的 MCP server 子进程，
// 使支持 OTel 的外部 server 把其 span 续接到 cogent 的同一条 trace 上（LOOP_SPEC §4.7）。
// 委托 observe 完成 OTel 细节（mcp 仅依赖 observe 薄接口，不直接 import OTel）。
// 无活跃 span 时不注入（优雅降级）；不支持 OTel 的 server 忽略未知 env，cogent 侧不受影响。
// 安全：traceparent/tracestate 仅含 trace/span id，无敏感信息（DEV_SPEC §7.5）。
func InjectTraceContext(ctx context.Context, env map[string]string) {
	if env == nil {
		return
	}
	carrier := make(map[string]string, 2)
	observe.InjectTraceContext(ctx, carrier)
	if tp := carrier["traceparent"]; tp != "" {
		env[envTraceparent] = tp
	}
	if ts := carrier["tracestate"]; ts != "" {
		env[envTracestate] = ts
	}
}
