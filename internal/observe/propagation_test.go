package observe

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestInjectTraceContext_WritesTraceparent(t *testing.T) {
	tid, err := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("trace id: %v", err)
	}
	sid, err := trace.SpanIDFromHex("0123456789abcdef")
	if err != nil {
		t.Fatalf("span id: %v", err)
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	carrier := map[string]string{}
	InjectTraceContext(ctx, carrier)

	want := "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
	if carrier["traceparent"] != want {
		t.Errorf("traceparent = %q, want %q", carrier["traceparent"], want)
	}
}

func TestInjectTraceContext_NoSpanLeavesCarrierEmpty(t *testing.T) {
	carrier := map[string]string{}
	InjectTraceContext(context.Background(), carrier)
	if len(carrier) != 0 {
		t.Errorf("carrier = %v, want empty for ctx without span", carrier)
	}
}

func TestInjectTraceContext_NilCarrierSafe(t *testing.T) {
	// 不应 panic。
	InjectTraceContext(context.Background(), nil)
}
