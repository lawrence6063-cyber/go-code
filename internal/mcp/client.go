package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/tool"
	"github.com/alaindong/cogent/internal/types"
)

// mcpProtocolVersion 是 initialize 握手声明的协议版本（广泛支持的稳定版本）。
const mcpProtocolVersion = "2024-11-05"

// clientVersion 是 cogent 作为 MCP 客户端的自报版本。
const clientVersion = "0.1.0"

// closeTimeout 是 Close 时等待 server 子进程自行退出的上限，超时则强杀。
const closeTimeout = 2 * time.Second

// serverNameRe 限定逻辑 server 名的字符集，既用于工具名前缀隔离，也防御注入。
var serverNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Transport 选择 MCP 客户端实现：本构建仅支持 builtin（自实现最小 stdio 客户端）。
type Transport string

// 传输实现枚举。
const (
	TransportBuiltin Transport = "builtin" // 自实现最小 stdio 客户端（默认）
)

// ServerConfig 描述一个待连接的 MCP server（stdio）。密钥仍从宿主环境注入，Env 仅附加非敏感配置。
type ServerConfig struct {
	Name    string            // 逻辑名，用作工具前缀 mcp__<Name>__
	Command string            // 可执行文件，如 "npx" 或 "python"
	Args    []string          // 启动参数
	Env     map[string]string // 附加环境变量（叠加在宿主 env 之上）
}

// validate 校验配置，拒绝非法 server 名（防工具名前缀污染）与空命令。
func (c ServerConfig) validate() error {
	if !serverNameRe.MatchString(c.Name) {
		return fmt.Errorf("invalid server name %q (allowed: letters, digits, _ and -)", c.Name)
	}
	if strings.TrimSpace(c.Command) == "" {
		return fmt.Errorf("empty command for server %q", c.Name)
	}
	return nil
}

// MCPClient 抽象一个 MCP server 连接：握手、暴露工具、释放。
type MCPClient interface {
	// Connect 启动 server 子进程并完成 initialize 握手与 tools/list。
	Connect(ctx context.Context, cfg ServerConfig) error
	// Tools 返回该 server 暴露的工具，命名 mcp__<server>__<tool>。
	Tools() []tool.Tool
	// Close 关闭连接并回收子进程。
	Close() error
}

// NewClient 按 transport 构造 MCP 客户端：仅支持 builtin（自实现最小 stdio 客户端）；
// 其余 transport 在本构建中不可用，返回明确错误而非静默回落。
func NewClient(transport Transport, tracer observe.Tracer) (MCPClient, error) {
	switch transport {
	case "", TransportBuiltin:
		return newBuiltinClient(tracer), nil
	default:
		return nil, fmt.Errorf("mcp transport %q not available (builtin only)", transport)
	}
}

// client 是 MCPClient 的自实现实现：持有 server 子进程与其 stdio 传输层。
type client struct {
	tracer observe.Tracer
	name   string
	cmd    *exec.Cmd
	tr     *transport
	tools  []tool.Tool
}

// newBuiltinClient 构造一个未连接的自实现客户端；tracer 为 nil 时回退到 no-op。
func newBuiltinClient(tracer observe.Tracer) *client {
	if tracer == nil {
		prov, _ := observe.New(observe.Config{Enabled: false})
		tracer = prov.Tracer()
	}
	return &client{tracer: tracer}
}

// Connect 见 MCPClient 接口说明。
func (c *client) Connect(ctx context.Context, cfg ServerConfig) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	c.name = cfg.Name
	cmd := exec.Command(cfg.Command, cfg.Args...)
	// 跨进程 trace 续链：把当前 span 的 W3C traceparent 注入子进程环境（无 span 时优雅降级）。
	env := cloneEnv(cfg.Env)
	InjectTraceContext(ctx, env)
	cmd.Env = mergeEnv(env)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("mcp %q stdin pipe: %w", cfg.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("mcp %q stdout pipe: %w", cfg.Name, err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start mcp server %q: %w", cfg.Name, err)
	}
	c.cmd = cmd
	c.tr = newTransport(stdout, stdin)
	if err := c.bootstrap(ctx); err != nil {
		_ = c.Close()
		return err
	}
	return nil
}

// bootstrap 执行 initialize 握手、initialized 通知与 tools/list 拉取。
func (c *client) bootstrap(ctx context.Context) error {
	params := initializeParams{
		ProtocolVersion: mcpProtocolVersion,
		Capabilities:    map[string]any{},
		ClientInfo:      implementation{Name: "cogent", Version: clientVersion},
	}
	if _, err := c.tr.call(ctx, "initialize", params); err != nil {
		return fmt.Errorf("mcp initialize %q: %w", c.name, err)
	}
	if err := c.tr.notify("notifications/initialized", struct{}{}); err != nil {
		return fmt.Errorf("mcp initialized %q: %w", c.name, err)
	}
	return c.loadTools(ctx)
}

// loadTools 拉取 server 工具清单并包装为 fail-closed 的远端工具。
func (c *client) loadTools(ctx context.Context) error {
	raw, err := c.tr.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return fmt.Errorf("mcp tools/list %q: %w", c.name, err)
	}
	var res toolsListResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("decode tools/list %q: %w", c.name, err)
	}
	c.tools = make([]tool.Tool, 0, len(res.Tools))
	for _, spec := range res.Tools {
		c.tools = append(c.tools, newRemoteTool(c, spec))
	}
	return nil
}

// callTool 调用远端工具并把结果规范化为 types.ToolResult。
func (c *client) callTool(ctx context.Context, origName string, args json.RawMessage) (types.ToolResult, error) {
	params := callToolParams{Name: origName, Arguments: normalizeArgs(args)}
	raw, err := c.tr.call(ctx, "tools/call", params)
	if err != nil {
		return types.ToolResult{}, err
	}
	var res callToolResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return types.ToolResult{}, fmt.Errorf("decode tools/call %q: %w", c.name, err)
	}
	return types.ToolResult{Content: res.text(), IsError: res.IsError}, nil
}

// Tools 见 MCPClient 接口说明。
func (c *client) Tools() []tool.Tool {
	out := make([]tool.Tool, len(c.tools))
	copy(out, c.tools)
	return out
}

// Close 见 MCPClient 接口说明：关闭写端 → 等待（超时强杀）子进程 → 等待读循环退出。
func (c *client) Close() error {
	if c.tr != nil {
		_ = c.tr.closeStdin()
	}
	err := c.waitProcess()
	if c.tr != nil {
		c.tr.waitDone()
	}
	return err
}

// waitProcess 等待子进程退出，超时则强制结束，确保不悬挂。
func (c *client) waitProcess() error {
	if c.cmd == nil || c.cmd.Process == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case <-done:
		return nil // 退出状态非零不视为错误（server 收到 EOF 退出）
	case <-time.After(closeTimeout):
		_ = c.cmd.Process.Kill()
		<-done
		return nil
	}
}

// mergeEnv 把附加环境变量叠加到宿主环境之上。
func mergeEnv(extra map[string]string) []string {
	env := os.Environ()
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// cloneEnv 复制配置 env，避免向调用方的 ServerConfig.Env 注入 traceparent 等运行期值。
func cloneEnv(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src)+2)
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// normalizeArgs 保证 arguments 始终是合法 JSON 对象（空入参补为 {}）。
func normalizeArgs(args json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(args))) == 0 {
		return json.RawMessage(`{}`)
	}
	return args
}

// initializeParams 是 initialize 请求参数。
type initializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      implementation `json:"clientInfo"`
}

// implementation 标识一个 MCP 端点的名称与版本。
type implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// toolSpec 是 tools/list 返回的单个工具声明。
type toolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// toolsListResult 是 tools/list 的结果体。
type toolsListResult struct {
	Tools []toolSpec `json:"tools"`
}

// callToolParams 是 tools/call 请求参数。
type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// contentBlock 是工具结果中的一个内容块（仅取文本类型）。
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// callToolResult 是 tools/call 的结果体。
type callToolResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError"`
}

// text 拼接所有文本内容块。
func (r callToolResult) text() string {
	parts := make([]string, 0, len(r.Content))
	for _, c := range r.Content {
		if c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}
