// Package observe 中的 exporter.go 按配置装配 OpenTelemetry 的 span/metric exporter：
// file（JSON 行落 data/traces）、stdout、otlp（gRPC，接 Jaeger/Tempo）三种后端。
package observe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// exporterDirPerms 与 exporterFilePerms 约束 trace 文件的目录/文件权限（不外泄）。
const (
	exporterDirPerms  = 0o700
	exporterFilePerms = 0o600
	otlpDialTimeout   = 5 * time.Second
)

// exporterSet 聚合一组 span/metric exporter 及其资源回收器。
type exporterSet struct {
	span   sdktrace.SpanExporter
	metric sdkmetric.Exporter
	closer func() error // 关闭底层文件句柄等；无则为 nil
}

// newExporterSet 依据 Config.Exporter 构造对应的 span/metric exporter。
func newExporterSet(ctx context.Context, cfg Config) (exporterSet, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Exporter)) {
	case "otlp":
		return newOTLPExporters(ctx, cfg.OTLPEndpoint)
	case "stdout":
		return newWriterExporters(os.Stdout, os.Stdout, nil)
	case "file", "":
		return newFileExporters(cfg.TraceDir)
	default:
		return exporterSet{}, fmt.Errorf("unknown trace exporter %q", cfg.Exporter)
	}
}

// newOTLPExporters 构造 gRPC OTLP span/metric exporter（本地明文连接 Jaeger/Tempo/collector）。
func newOTLPExporters(ctx context.Context, endpoint string) (exporterSet, error) {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = "localhost:4317"
	}
	dialCtx, cancel := context.WithTimeout(ctx, otlpDialTimeout)
	defer cancel()
	span, err := otlptracegrpc.New(dialCtx,
		otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	if err != nil {
		return exporterSet{}, fmt.Errorf("otlp trace exporter: %w", err)
	}
	metricExp, err := otlpmetricgrpc.New(dialCtx,
		otlpmetricgrpc.WithEndpoint(endpoint), otlpmetricgrpc.WithInsecure())
	if err != nil {
		return exporterSet{}, fmt.Errorf("otlp metric exporter: %w", err)
	}
	return exporterSet{span: span, metric: metricExp}, nil
}

// newFileExporters 把 span/metric 以 JSON 行写入 traceDir 下带时间戳的文件。
func newFileExporters(traceDir string) (exporterSet, error) {
	if strings.TrimSpace(traceDir) == "" {
		traceDir = filepath.Join("data", "traces")
	}
	if err := os.MkdirAll(traceDir, exporterDirPerms); err != nil {
		return exporterSet{}, fmt.Errorf("create trace dir: %w", err)
	}
	stamp := time.Now().Format("20060102-150405")
	traceFile, err := openTraceFile(traceDir, "traces-"+stamp+".jsonl")
	if err != nil {
		return exporterSet{}, err
	}
	metricFile, err := openTraceFile(traceDir, "metrics-"+stamp+".jsonl")
	if err != nil {
		_ = traceFile.Close()
		return exporterSet{}, err
	}
	closer := func() error { return errors.Join(traceFile.Close(), metricFile.Close()) }
	return newWriterExporters(traceFile, metricFile, closer)
}

// openTraceFile 在 dir 内以 0o600 追加打开（或创建）一个 trace 文件，名称经清洗防穿越。
func openTraceFile(dir, name string) (*os.File, error) {
	clean := filepath.Join(dir, filepath.Base(name))
	f, err := os.OpenFile(clean, os.O_CREATE|os.O_WRONLY|os.O_APPEND, exporterFilePerms)
	if err != nil {
		return nil, fmt.Errorf("open trace file: %w", err)
	}
	return f, nil
}

// newWriterExporters 用 stdout 系列 exporter 把 span/metric 写到给定 writer。
func newWriterExporters(traceW, metricW io.Writer, closer func() error) (exporterSet, error) {
	span, err := stdouttrace.New(stdouttrace.WithWriter(traceW))
	if err != nil {
		return exporterSet{}, fmt.Errorf("stdout trace exporter: %w", err)
	}
	metricExp, err := stdoutmetric.New(stdoutmetric.WithWriter(metricW))
	if err != nil {
		return exporterSet{}, fmt.Errorf("stdout metric exporter: %w", err)
	}
	return exporterSet{span: span, metric: metricExp, closer: closer}, nil
}
