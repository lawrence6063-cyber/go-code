// Package observe 对可观测能力（trace/指标）做薄封装，向内核屏蔽 OpenTelemetry 细节。
// Enabled=false 或 Exporter=none 时返回零开销 no-op 实现；否则返回 OTel SDK 支撑的实现（见 otel.go）。
package observe

import (
	"context"
	"strings"
)

// Attr 是一个 span/metric 属性键值对（对 OTel attribute.KeyValue 的轻封装）。
type Attr struct {
	Key   string // 属性键
	Value any    // 属性值
}

// EndFunc 结束当前 span；传入非 nil error 时自动记录错误与失败状态。
type EndFunc func(err error)

// Tracer 开启一个 span，返回派生 ctx（携带新 span）与结束函数。
// 内核统一用法：ctx, end := tr.Start(ctx, "react.step"); defer end(err)
type Tracer interface {
	Start(ctx context.Context, name string, attrs ...Attr) (context.Context, EndFunc)
}

// Meter 采集计量指标（计数器 / 直方图）。
type Meter interface {
	Count(name string, delta int64, attrs ...Attr)
	Record(name string, value float64, attrs ...Attr) // 直方图（耗时/分布）
}

// Provider 在启动期按配置装配 Tracer/Meter 与 exporter，运行期不可变；
// Shutdown 在退出前 flush 缓冲的 span（确保最后一批不丢）。
type Provider interface {
	Tracer() Tracer
	Meter() Meter
	Shutdown(ctx context.Context) error
}

// Config 配置可观测后端；由 cmd 层按 env 构造。
type Config struct {
	Enabled      bool    // 总开关；false 时 New 返回 no-op Provider
	Exporter     string  // "file" | "otlp" | "none"
	TraceDir     string  // exporter=file 时的输出目录
	OTLPEndpoint string  // exporter=otlp 时的地址
	SampleRatio  float64 // 采样率 0.0~1.0
}

// New 依据配置构造 Provider：Enabled=false 或 Exporter 为空/none 时返回零开销 no-op；
// 否则按 Exporter（file/stdout/otlp）装配 OTel SDK 支撑的实现。
func New(cfg Config) (Provider, error) {
	if !cfg.Enabled {
		return noopProvider{}, nil
	}
	if e := strings.ToLower(strings.TrimSpace(cfg.Exporter)); e == "none" {
		return noopProvider{}, nil
	}
	return newOTelProvider(cfg)
}

// noopProvider 是 Provider 的零开销空实现。
type noopProvider struct{}

func (noopProvider) Tracer() Tracer { return noopTracer{} }

func (noopProvider) Meter() Meter { return noopMeter{} }

func (noopProvider) Shutdown(context.Context) error { return nil }

// noopTracer 是 Tracer 的空实现：原样返回 ctx 与空 EndFunc。
type noopTracer struct{}

func (noopTracer) Start(ctx context.Context, _ string, _ ...Attr) (context.Context, EndFunc) {
	return ctx, func(error) {}
}

// noopMeter 是 Meter 的空实现。
type noopMeter struct{}

func (noopMeter) Count(string, int64, ...Attr) {}

func (noopMeter) Record(string, float64, ...Attr) {}
