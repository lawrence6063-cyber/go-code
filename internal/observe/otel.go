// Package observe 中的 otel.go 用 OpenTelemetry SDK 实现 Provider/Tracer/Meter。
// 关键约束：OTel 类型仅出现在本包内部实现，绝不泄漏到导出接口（Tracer/Meter/Provider/Attr），
// 使内核各层只依赖 observe 薄接口、对 OTel 无感，保持解耦、可单测、可替换（DEV_SPEC §4.4/§5.11）。
package observe

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// 服务标识常量，写入 Resource，便于在 Jaeger/Tempo 中区分来源。
const (
	serviceName    = "cogent"
	serviceVersion = "0.1.0"
	tracerName     = "github.com/alaindong/cogent"
)

// newOTelProvider 依据 Config 装配真实的 TracerProvider/MeterProvider 及其 exporter。
func newOTelProvider(cfg Config) (Provider, error) {
	ctx := context.Background()
	exp, err := newExporterSet(ctx, cfg)
	if err != nil {
		return nil, err
	}
	res := resource.NewSchemaless(
		attribute.String("service.name", serviceName),
		attribute.String("service.version", serviceVersion),
	)
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp.span),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(samplerFor(cfg.SampleRatio)),
	)
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp.metric)),
		sdkmetric.WithResource(res),
	)
	return &otelProvider{
		tp:     tp,
		mp:     mp,
		tracer: &otelTracer{tracer: tp.Tracer(tracerName)},
		meter:  newOTelMeter(mp.Meter(tracerName)),
		closer: exp.closer,
	}, nil
}

// samplerFor 把采样率映射为采样器：>=1 全采、<=0 全采（默认开），其余按 TraceID 比例采样（继承父决策）。
func samplerFor(ratio float64) sdktrace.Sampler {
	if ratio >= 1 || ratio <= 0 {
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	}
	return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
}

// otelProvider 是基于 OTel SDK 的 Provider 实现。
type otelProvider struct {
	tp     *sdktrace.TracerProvider
	mp     *sdkmetric.MeterProvider
	tracer Tracer
	meter  Meter
	closer func() error
}

// Tracer 见 Provider 接口说明。
func (p *otelProvider) Tracer() Tracer { return p.tracer }

// Meter 见 Provider 接口说明。
func (p *otelProvider) Meter() Meter { return p.meter }

// Shutdown 顺序 flush trace 与 metric（确保最后一批不丢），再关闭底层文件句柄。
func (p *otelProvider) Shutdown(ctx context.Context) error {
	var errs []error
	if err := p.tp.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("trace shutdown: %w", err))
	}
	if err := p.mp.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("metric shutdown: %w", err))
	}
	if p.closer != nil {
		if err := p.closer(); err != nil {
			errs = append(errs, fmt.Errorf("close exporter: %w", err))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

// otelTracer 是基于 OTel SDK 的 Tracer 实现。
type otelTracer struct {
	tracer trace.Tracer
}

// Start 开启一个 span，返回派生 ctx 与结束函数；end(err) 非 nil 时记录错误与失败状态。
func (t *otelTracer) Start(ctx context.Context, name string, attrs ...Attr) (context.Context, EndFunc) {
	ctx, span := t.tracer.Start(ctx, name)
	if len(attrs) > 0 {
		span.SetAttributes(toKeyValues(attrs)...)
	}
	return ctx, func(err error) {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}
}

// otelMeter 是基于 OTel SDK 的 Meter 实现：惰性创建并缓存 counter/histogram 仪表。
type otelMeter struct {
	meter    metric.Meter
	mu       sync.Mutex
	counters map[string]metric.Int64Counter
	hists    map[string]metric.Float64Histogram
}

// newOTelMeter 用底层 OTel Meter 构造缓存型 Meter。
func newOTelMeter(m metric.Meter) *otelMeter {
	return &otelMeter{
		meter:    m,
		counters: make(map[string]metric.Int64Counter),
		hists:    make(map[string]metric.Float64Histogram),
	}
}

// Count 见 Meter 接口说明。
func (m *otelMeter) Count(name string, delta int64, attrs ...Attr) {
	c, err := m.counter(name)
	if err != nil {
		return
	}
	c.Add(context.Background(), delta, metric.WithAttributes(toKeyValues(attrs)...))
}

// Record 见 Meter 接口说明。
func (m *otelMeter) Record(name string, value float64, attrs ...Attr) {
	h, err := m.histogram(name)
	if err != nil {
		return
	}
	h.Record(context.Background(), value, metric.WithAttributes(toKeyValues(attrs)...))
}

// counter 返回（必要时创建）指定名称的 Int64Counter。
func (m *otelMeter) counter(name string) (metric.Int64Counter, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.counters[name]; ok {
		return c, nil
	}
	c, err := m.meter.Int64Counter(name)
	if err != nil {
		return nil, err
	}
	m.counters[name] = c
	return c, nil
}

// histogram 返回（必要时创建）指定名称的 Float64Histogram。
func (m *otelMeter) histogram(name string) (metric.Float64Histogram, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h, ok := m.hists[name]; ok {
		return h, nil
	}
	h, err := m.meter.Float64Histogram(name)
	if err != nil {
		return nil, err
	}
	m.hists[name] = h
	return h, nil
}

// toKeyValues 把 observe.Attr 列表转换为 OTel attribute.KeyValue 列表。
func toKeyValues(attrs []Attr) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		out = append(out, toKeyValue(a))
	}
	return out
}

// toKeyValue 把单个 Attr 转换为 attribute.KeyValue，按值类型选择对应构造器，其余类型字符串兜底。
func toKeyValue(a Attr) attribute.KeyValue {
	switch v := a.Value.(type) {
	case string:
		return attribute.String(a.Key, v)
	case bool:
		return attribute.Bool(a.Key, v)
	case int:
		return attribute.Int(a.Key, v)
	case int64:
		return attribute.Int64(a.Key, v)
	case float64:
		return attribute.Float64(a.Key, v)
	default:
		return attribute.String(a.Key, fmt.Sprint(v))
	}
}
