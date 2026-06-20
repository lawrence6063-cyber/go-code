package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// 本文件提供一套 MCP 协议一致性套件与一个最小的、协议兼容的 fake MCP server，仅供测试使用：
// 既验证 builtin 客户端「连接 → 列工具 → 调用 → 错误结果」的协议契约，也借测试二进制自执行
// （见 maybeRunFakeServer）充当被连接的 server，无需依赖任何外部进程。

// 协议常量与 fake server 暴露的工具名。
const (
	conformProtoVersion = "2024-11-05"             // fake server 缺省回报的协议版本
	echoToolName        = "echo"                   // 回显入参 text 的工具
	boomToolName        = "boom"                   // 恒定返回错误结果、用于验证 isError 通路的工具
	envFakeServer       = "COGENT_MCP_FAKE_SERVER" // 置为 "1" 时进程以 fake server 身份运行
)

// connSpec 描述如何启动并连接被测 server（由测试二进制自执行充当 server）。
type connSpec struct {
	Name    string            // 逻辑 server 名
	Command string            // 可执行文件路径
	Args    []string          // 启动参数
	Env     map[string]string // 附加环境变量
}

// conformClient 是套件所需的最小能力面，屏蔽实现的具体 API 差异。
type conformClient interface {
	ToolNames() []string
	Call(ctx context.Context, tool string, args map[string]any) (text string, isErr bool, err error)
	Close() error
}

// connector 按 spec 建立并连接一个 conformClient。
type connector func(ctx context.Context, spec connSpec) (conformClient, error)

// fakeServerSpec 返回让当前测试二进制自执行充当 fake server 的连接规格。
func fakeServerSpec(name string) connSpec {
	return connSpec{
		Name:    name,
		Command: os.Args[0],
		Env:     map[string]string{envFakeServer: "1"},
	}
}

// maybeRunFakeServer 若检测到 envFakeServer，则以 fake server 身份运行并返回 true。
// TestMain 应在最前调用：if maybeRunFakeServer() { return }。
func maybeRunFakeServer() bool {
	if os.Getenv(envFakeServer) != "1" {
		return false
	}
	_ = runFakeServer(os.Stdin, os.Stdout)
	return true
}

// runConformanceSuite 用给定 connector 驱动「连接 → 列工具 → 调用 → 错误结果」一致性断言。
func runConformanceSuite(t *testing.T, connect connector) {
	t.Helper()
	ctx := context.Background()
	cl, err := connect(ctx, fakeServerSpec("test"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() {
		if cerr := cl.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()

	t.Run("ListsTools", func(t *testing.T) {
		names := cl.ToolNames()
		if !hasTool(names, echoToolName) {
			t.Errorf("echo tool missing in %v", names)
		}
		if !hasTool(names, boomToolName) {
			t.Errorf("boom tool missing in %v", names)
		}
	})
	t.Run("CallEcho", func(t *testing.T) {
		text, isErr, err := cl.Call(ctx, echoToolName, map[string]any{"text": "hi"})
		if err != nil {
			t.Fatalf("call echo: %v", err)
		}
		if isErr {
			t.Errorf("echo unexpectedly returned error result")
		}
		if !strings.Contains(text, "hi") {
			t.Errorf("echo text=%q, want it to contain %q", text, "hi")
		}
	})
	t.Run("CallBoomIsError", func(t *testing.T) {
		_, isErr, err := cl.Call(ctx, boomToolName, nil)
		if err != nil {
			t.Fatalf("call boom: %v", err)
		}
		if !isErr {
			t.Errorf("boom should yield an error result (isError=true)")
		}
	})
}

// hasTool 判断工具列表是否包含某基础工具名（兼容带 mcp__server__ 前缀的命名）。
func hasTool(names []string, base string) bool {
	for _, n := range names {
		if n == base || strings.HasSuffix(n, "__"+base) {
			return true
		}
	}
	return false
}

// --- fake MCP server（协议兼容的最小实现）---

// rpcIn 是入站消息；ID 用 RawMessage 原样回显（兼容数字/字符串两种形态）。
type rpcIn struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params"`
}

// rpcErr 是 JSON-RPC 错误对象。
type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// rpcOut 是出站响应。
type rpcOut struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

// runFakeServer 以换行分隔 JSON-RPC 2.0 在 r/w 上提供一个最小 MCP server，直至 r 读到 EOF。
func runFakeServer(r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	enc := json.NewEncoder(w) // Encode 自动追加换行，天然满足换行分隔
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var in rpcIn
		if err := json.Unmarshal([]byte(line), &in); err != nil {
			continue
		}
		if in.ID == nil {
			continue // 通知无需响应
		}
		if err := enc.Encode(handle(in)); err != nil {
			return err
		}
	}
	return sc.Err()
}

// handle 按方法名分发并构造响应。
func handle(in rpcIn) rpcOut {
	out := rpcOut{JSONRPC: "2.0", ID: *in.ID}
	switch in.Method {
	case "initialize":
		out.Result = initializeResult(in.Params)
	case "tools/list":
		out.Result = toolsList()
	case "tools/call":
		res, rerr := callTool(in.Params)
		out.Result, out.Error = res, rerr
	case "ping":
		out.Result = map[string]any{}
	default:
		out.Error = &rpcErr{Code: -32601, Message: "method not found: " + in.Method}
	}
	return out
}

// initializeResult 回报能力声明，并回显客户端请求的协议版本（最大化兼容性）。
func initializeResult(params json.RawMessage) map[string]any {
	ver := conformProtoVersion
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(params, &p) == nil && p.ProtocolVersion != "" {
		ver = p.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": ver,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "cogent-fake-mcp", "version": "0.1.0"},
	}
}

// toolsList 返回 echo/boom 两个工具的声明。
func toolsList() map[string]any {
	textSchema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"text": map[string]any{"type": "string"}},
	}
	return map[string]any{"tools": []map[string]any{
		{"name": echoToolName, "description": "echo back the text argument", "inputSchema": textSchema},
		{"name": boomToolName, "description": "always returns an error result", "inputSchema": map[string]any{"type": "object"}},
	}}
}

// callTool 执行 echo/boom，返回内容块或 JSON-RPC 错误。
func callTool(params json.RawMessage) (map[string]any, *rpcErr) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcErr{Code: -32602, Message: "invalid params"}
	}
	switch p.Name {
	case echoToolName:
		text, _ := p.Arguments["text"].(string)
		return textResult("echo: "+text, false), nil
	case boomToolName:
		return textResult("boom failed", true), nil
	default:
		return nil, &rpcErr{Code: -32602, Message: "unknown tool " + p.Name}
	}
}

// textResult 构造一个含单个文本内容块的工具结果。
func textResult(text string, isErr bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isErr,
	}
}
