package mcp

import (
	"context"
	"regexp"
	"testing"

	"github.com/alaindong/cogent/internal/observe"
)

// traceparentRe 校验 W3C traceparent 格式：00-<32hex>-<16hex>-<2hex>。
var traceparentRe = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`)

func TestInjectTraceContext_SetsTraceparentEnv(t *testing.T) {
	prov, err := observe.New(observe.Config{
		Enabled:     true,
		Exporter:    "file",
		TraceDir:    t.TempDir(),
		SampleRatio: 1,
	})
	if err != nil {
		t.Fatalf("observe.New: %v", err)
	}
	t.Cleanup(func() { _ = prov.Shutdown(context.Background()) })

	ctx, end := prov.Tracer().Start(context.Background(), "test.span")
	defer end(nil)

	env := map[string]string{}
	InjectTraceContext(ctx, env)

	tp := env[envTraceparent]
	if tp == "" {
		t.Fatal("TRACEPARENT not injected for active span")
	}
	if !traceparentRe.MatchString(tp) {
		t.Errorf("TRACEPARENT = %q, want W3C traceparent format", tp)
	}
}

func TestInjectTraceContext_NoSpanNoEnv(t *testing.T) {
	env := map[string]string{}
	InjectTraceContext(context.Background(), env)
	if len(env) != 0 {
		t.Errorf("env = %v, want empty for ctx without span (graceful degrade)", env)
	}
}

func TestInjectTraceContext_NilEnvSafe(t *testing.T) {
	InjectTraceContext(context.Background(), nil) // 不应 panic
}
