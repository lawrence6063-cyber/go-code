// Package observe 中的 slog.go 提供把当前 span 的 trace_id/span_id 注入结构化日志的 Handler，
// 使日志与 trace 互相对齐（日志能跳到对应 span，便于坏 case 联合排查，DEV_SPEC §8.6）。
package observe

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// traceHandler 包装一个 slog.Handler，在每条日志记录上补充当前 ctx 的 trace_id/span_id。
type traceHandler struct {
	inner slog.Handler
}

// NewTraceLogHandler 用给定底层 Handler 构造一个会注入 trace 关联字段的 slog.Handler。
func NewTraceLogHandler(inner slog.Handler) slog.Handler {
	return traceHandler{inner: inner}
}

// Enabled 透传底层 Handler 的级别判定。
func (h traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle 从 ctx 取出有效的 span 上下文并注入 trace_id/span_id，再委托底层 Handler 输出。
func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.inner.Handle(ctx, r)
}

// WithAttrs 返回带附加属性的新 Handler，并保持 trace 注入能力。
func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{inner: h.inner.WithAttrs(attrs)}
}

// WithGroup 返回带分组的新 Handler，并保持 trace 注入能力。
func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{inner: h.inner.WithGroup(name)}
}
