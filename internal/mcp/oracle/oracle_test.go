package oracle

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alaindong/cogent/internal/mcp/mcpconform"
)

// TestMain 在最前路由 fake server 自执行（与主模块共用同一 fake server 与套件），否则跑常规测试。
func TestMain(m *testing.M) {
	if mcpconform.MaybeRunFakeServer() {
		return
	}
	os.Exit(m.Run())
}

// sdkAdapter 把官方 SDK 的会话适配为一致性套件所需的最小 Client。
type sdkAdapter struct {
	session *mcpsdk.ClientSession
	names   []string
}

// ToolNames 返回 SDK 发现的工具名。
func (a *sdkAdapter) ToolNames() []string { return a.names }

// Call 经官方 SDK 调用工具并拼接文本内容。
func (a *sdkAdapter) Call(
	ctx context.Context,
	name string,
	args map[string]any,
) (string, bool, error) {
	res, err := a.session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return "", false, err
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String(), res.IsError, nil
}

// Close 关闭 SDK 会话。
func (a *sdkAdapter) Close() error { return a.session.Close() }

// TestSDKClient_Conformance 用官方 SDK 客户端跑与自实现完全相同的一致性套件。
func TestSDKClient_Conformance(t *testing.T) {
	connect := func(ctx context.Context, spec mcpconform.ConnSpec) (mcpconform.Client, error) {
		client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "cogent-oracle", Version: "0.1.0"}, nil)
		cmd := exec.Command(spec.Command, spec.Args...)
		cmd.Env = append(os.Environ(), envSlice(spec.Env)...)
		session, err := client.Connect(ctx, &mcpsdk.CommandTransport{Command: cmd}, nil)
		if err != nil {
			return nil, err
		}
		lt, err := session.ListTools(ctx, nil)
		if err != nil {
			_ = session.Close()
			return nil, err
		}
		names := make([]string, 0, len(lt.Tools))
		for _, tl := range lt.Tools {
			names = append(names, tl.Name)
		}
		return &sdkAdapter{session: session, names: names}, nil
	}
	mcpconform.RunSuite(t, connect)
}

// envSlice 把环境变量映射转为 KEY=VALUE 切片。
func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
