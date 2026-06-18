// Command cogent 是自主编码 Agent 运行时的 CLI 入口。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/alaindong/cogent/internal/observe"
)

func main() {
	logger := newLogger(os.Getenv("COGENT_LOG_LEVEL"))
	slog.SetDefault(logger)

	// 把 Ctrl-C(SIGINT)/SIGTERM 转为 ctx 取消，实现全链路优雅退出。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "cogent:", err)
		os.Exit(1)
	}
}

// newLogger 按级别字符串构造结构化日志器；无法识别时回退到 info。
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	// 包一层 trace 注入：日志自动带上当前 span 的 trace_id/span_id，与 trace 对齐（Phase 8）。
	return slog.New(observe.NewTraceLogHandler(handler))
}
