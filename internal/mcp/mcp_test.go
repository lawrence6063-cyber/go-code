package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/alaindong/cogent/internal/tool"
)

// TestMain 在最前路由 fake server 自执行，否则跑常规测试并检测 goroutine 泄漏。
func TestMain(m *testing.M) {
	if maybeRunFakeServer() {
		return
	}
	goleak.VerifyTestMain(m)
}

// nopSink 是无操作的进度接收器。
type nopSink struct{}

// Emit 丢弃进度文本。
func (nopSink) Emit(string) {}

// builtinAdapter 把自实现 MCPClient 适配为一致性套件所需的最小 conformClient。
type builtinAdapter struct {
	cl MCPClient
}

// ToolNames 返回客户端暴露的（带前缀的）工具名。
func (a *builtinAdapter) ToolNames() []string {
	tools := a.cl.Tools()
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name())
	}
	return names
}

// Call 按基础名定位带前缀的工具并执行。
func (a *builtinAdapter) Call(
	ctx context.Context,
	name string,
	args map[string]any,
) (string, bool, error) {
	var target tool.Tool
	for _, t := range a.cl.Tools() {
		if strings.HasSuffix(t.Name(), "__"+name) {
			target = t
			break
		}
	}
	if target == nil {
		return "", false, fmt.Errorf("tool %q not found", name)
	}
	input, err := json.Marshal(args)
	if err != nil {
		return "", false, err
	}
	res, err := target.Call(ctx, input, nopSink{})
	if err != nil {
		return "", false, err
	}
	return res.Content, res.IsError, nil
}

// Close 释放底层连接。
func (a *builtinAdapter) Close() error { return a.cl.Close() }

// TestBuiltinClient_Conformance 用共享一致性套件验证自实现客户端的协议行为。
func TestBuiltinClient_Conformance(t *testing.T) {
	connect := func(ctx context.Context, spec connSpec) (conformClient, error) {
		cl, err := NewClient(TransportBuiltin, nil)
		if err != nil {
			return nil, err
		}
		cfg := ServerConfig{Name: spec.Name, Command: spec.Command, Args: spec.Args, Env: spec.Env}
		if err := cl.Connect(ctx, cfg); err != nil {
			return nil, err
		}
		return &builtinAdapter{cl: cl}, nil
	}
	runConformanceSuite(t, connect)
}

// TestNewClient_UnknownTransport 验证未知 transport 返回明确错误（不静默回落）。
func TestNewClient_UnknownTransport(t *testing.T) {
	if _, err := NewClient("sdk", nil); err == nil {
		t.Error("expected error for sdk transport in this build, got nil")
	}
}

// TestServerConfig_Validate 表驱动校验 server 名与命令的合法性。
func TestServerConfig_Validate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     ServerConfig
		wantErr bool
	}{
		{"ok", ServerConfig{Name: "fs-1", Command: "npx"}, false},
		{"empty name", ServerConfig{Name: "", Command: "npx"}, true},
		{"bad name", ServerConfig{Name: "fs server", Command: "npx"}, true},
		{"path traversal name", ServerConfig{Name: "../etc", Command: "npx"}, true},
		{"empty command", ServerConfig{Name: "fs", Command: "  "}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("validate() err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

// TestToolName 验证工具名前缀拼接。
func TestToolName(t *testing.T) {
	if got := ToolName("fs", "read"); got != "mcp__fs__read" {
		t.Errorf("ToolName = %q, want mcp__fs__read", got)
	}
}

// TestLoadConfig 验证缺省文件、正常解析与非法 server 名三种路径。
func TestLoadConfig(t *testing.T) {
	t.Run("missing file returns nil", func(t *testing.T) {
		cfgs, err := LoadConfig(t.TempDir())
		if err != nil {
			t.Fatalf("LoadConfig missing: %v", err)
		}
		if cfgs != nil {
			t.Errorf("want nil cfgs, got %v", cfgs)
		}
	})

	t.Run("parses servers sorted", func(t *testing.T) {
		root := t.TempDir()
		writeMCPConfig(t, root, `{"mcpServers":{"beta":{"command":"b"},"alpha":{"command":"a","args":["x"]}}}`)
		cfgs, err := LoadConfig(root)
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if len(cfgs) != 2 || cfgs[0].Name != "alpha" || cfgs[1].Name != "beta" {
			t.Fatalf("unexpected cfgs: %#v", cfgs)
		}
		if cfgs[0].Command != "a" || len(cfgs[0].Args) != 1 {
			t.Errorf("alpha mis-parsed: %#v", cfgs[0])
		}
	})

	t.Run("rejects invalid server name", func(t *testing.T) {
		root := t.TempDir()
		writeMCPConfig(t, root, `{"mcpServers":{"bad name":{"command":"x"}}}`)
		if _, err := LoadConfig(root); err == nil {
			t.Error("expected error for invalid server name, got nil")
		}
	})
}

// writeMCPConfig 在 root/.cogent/mcp.json 写入给定内容。
func writeMCPConfig(t *testing.T, root, content string) {
	t.Helper()
	dir := filepath.Join(root, ".cogent")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
