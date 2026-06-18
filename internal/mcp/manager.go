package mcp

import (
	"context"
	"errors"
	"log/slog"

	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/tool"
)

// Manager 连接并聚合多个 MCP server：单 server 连接失败仅告警跳过，不阻断 cogent 启动
// （MCP 为可插拔增强，非硬依赖）。聚合得到的工具供 cmd 层置于内建工具之后注入工具池。
type Manager struct {
	transport Transport
	tracer    observe.Tracer
	clients   []MCPClient
}

// NewManager 构造一个使用指定 transport 的管理器。
func NewManager(transport Transport, tracer observe.Tracer) *Manager {
	return &Manager{transport: transport, tracer: tracer}
}

// Connect 依次连接给定的 server 配置；逐个容错：单个失败记录告警并跳过。
func (m *Manager) Connect(ctx context.Context, cfgs []ServerConfig) {
	for _, cfg := range cfgs {
		m.connectOne(ctx, cfg)
	}
}

// connectOne 连接单个 server，失败时清理并告警跳过。
func (m *Manager) connectOne(ctx context.Context, cfg ServerConfig) {
	cl, err := NewClient(m.transport, m.tracer)
	if err != nil {
		slog.Warn("mcp client init failed", "server", cfg.Name, "err", err)
		return
	}
	if err := cl.Connect(ctx, cfg); err != nil {
		slog.Warn("mcp connect failed", "server", cfg.Name, "err", err)
		_ = cl.Close()
		return
	}
	m.clients = append(m.clients, cl)
	slog.Info("mcp server connected", "server", cfg.Name, "tools", len(cl.Tools()))
}

// Tools 聚合所有已连接 server 暴露的工具。
func (m *Manager) Tools() []tool.Tool {
	var out []tool.Tool
	for _, cl := range m.clients {
		out = append(out, cl.Tools()...)
	}
	return out
}

// Close 关闭所有连接并汇总错误。
func (m *Manager) Close() error {
	var errs []error
	for _, cl := range m.clients {
		if err := cl.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
