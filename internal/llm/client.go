// Package llm 抽象 LLM 提供方，默认实现走 DeepSeek 的 OpenAI 兼容接口。
// 通过 channel 把流式增量向上游 yield；密钥仅来自环境变量，严禁硬编码。
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	openai "github.com/sashabaranov/go-openai"

	"github.com/alaindong/cogent/internal/types"
)

// Client 抽象 LLM 提供方；默认实现走 DeepSeek 的 OpenAI 兼容接口。
type Client interface {
	// Stream 发起一次流式补全，通过 channel 返回增量；ctx 取消即中止请求。
	Stream(ctx context.Context, req Request) (<-chan Delta, error)
}

// ToolSchema 是一个工具的 function calling 声明（名称 + 描述 + 入参 JSON Schema）。
type ToolSchema struct {
	Name        string          // 工具名
	Description string          // 工具描述
	Parameters  json.RawMessage // 入参 JSON Schema
}

// Request 是一次补全请求。
type Request struct {
	Messages    []types.Message // 上下文消息列表
	Tools       []ToolSchema    // function calling 工具声明（Phase 1 暂为空）
	Model       string          // 模型名
	Temperature float32         // 采样温度；0 表示用提供方默认
	MaxTokens   int             // 单次输出上限；0 表示用提供方默认
}

// Delta 是流式响应的一个增量片段。
type Delta struct {
	Text     string              // 文本增量
	ToolCall *types.ToolUseBlock // 工具调用（Phase 2 起有效）
	Usage    *Usage              // 末尾增量携带 token 计量
	Err      error               // 流中错误
}

// Usage 记录一次调用的 token 消耗。
type Usage struct {
	PromptTokens     int // 输入 token 数
	CompletionTokens int // 输出 token 数
}

// Config 配置 LLM 客户端连接参数；由 cmd 层按 env 构造后注入。
type Config struct {
	APIKey  string // 密钥，仅来自环境变量，严禁硬编码
	BaseURL string // OpenAI 兼容接口 BaseURL（DeepSeek）
}

// New 构造一个走 DeepSeek OpenAI 兼容接口的 Client；APIKey 为空时启动期 fail-fast。
func New(cfg Config) (Client, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("missing api key (set DEEPSEEK_API_KEY)")
	}
	oc := openai.DefaultConfig(cfg.APIKey)
	if cfg.BaseURL != "" {
		oc.BaseURL = cfg.BaseURL
	}
	return &client{api: openai.NewClientWithConfig(oc)}, nil
}

// client 是 Client 的 DeepSeek OpenAI 兼容实现。
type client struct {
	api *openai.Client
}

// Stream 发起一次流式补全，启动后台 goroutine 把 SSE 增量泵入 channel。
func (c *client) Stream(ctx context.Context, req Request) (<-chan Delta, error) {
	stream, err := c.api.CreateChatCompletionStream(ctx, toOpenAIRequest(req))
	if err != nil {
		return nil, fmt.Errorf("create stream: %w", err)
	}
	out := make(chan Delta, 16)
	go func() {
		defer close(out)
		defer func() { _ = stream.Close() }()
		pump(ctx, stream, out)
	}()
	return out, nil
}

// pump 循环读取 SSE 增量并上抛 Delta；按 index 累积流式 tool_calls，
// 待 finish_reason=tool_calls（或流结束兜底）时组装为完整 ToolUseBlock。
// io.EOF 为正常结束，ctx 取消即收手。
func pump(ctx context.Context, stream *openai.ChatCompletionStream, out chan<- Delta) {
	acc := newToolAcc()
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			_ = acc.flush(ctx, out) // 兜底：流结束前刷出尚未上抛的工具调用
			return
		}
		if err != nil {
			emit(ctx, out, Delta{Err: fmt.Errorf("recv: %w", err)})
			return
		}
		if !processFrame(ctx, resp, acc, out) {
			return
		}
	}
}

// processFrame 处理一帧 SSE：上抛文本与 usage、累积 tool_calls 分片，
// finish_reason=tool_calls 时刷出已集齐的工具调用。返回 false 表示 ctx 已取消。
func processFrame(ctx context.Context, resp openai.ChatCompletionStreamResponse, acc *toolAcc, out chan<- Delta) bool {
	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		if txt := choice.Delta.Content; txt != "" {
			if !emit(ctx, out, Delta{Text: txt}) {
				return false
			}
		}
		acc.add(choice.Delta.ToolCalls)
		if choice.FinishReason == openai.FinishReasonToolCalls && !acc.flush(ctx, out) {
			return false
		}
	}
	if resp.Usage != nil {
		u := &Usage{PromptTokens: resp.Usage.PromptTokens, CompletionTokens: resp.Usage.CompletionTokens}
		if !emit(ctx, out, Delta{Usage: u}) {
			return false
		}
	}
	return true
}

// emit 在尊重 ctx 取消的前提下把 Delta 送入 channel；返回 false 表示已取消。
func emit(ctx context.Context, out chan<- Delta, d Delta) bool {
	select {
	case <-ctx.Done():
		return false
	case out <- d:
		return true
	}
}

// toOpenAIRequest 把 cogent 的 Request 映射为 go-openai 的流式请求。
func toOpenAIRequest(req Request) openai.ChatCompletionRequest {
	msgs := make([]openai.ChatCompletionMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, toOpenAIMessage(m))
	}
	r := openai.ChatCompletionRequest{
		Model:         req.Model,
		Messages:      msgs,
		Temperature:   req.Temperature,
		MaxTokens:     req.MaxTokens,
		Stream:        true,
		StreamOptions: &openai.StreamOptions{IncludeUsage: true},
	}
	if len(req.Tools) > 0 {
		r.Tools = toOpenAITools(req.Tools)
	}
	return r
}

// toOpenAIMessage 映射单条消息，保证 function calling 的 tool_calls 与 tool 配对完整。
func toOpenAIMessage(m types.Message) openai.ChatCompletionMessage {
	msg := openai.ChatCompletionMessage{Role: mapRole(m.Role), Content: m.Text}
	if m.Role == types.RoleTool {
		msg.ToolCallID = m.ToolUseID
		msg.Name = m.ToolName
	}
	if len(m.ToolCalls) > 0 {
		msg.ToolCalls = toOpenAIToolCalls(m.ToolCalls)
	}
	return msg
}

// toOpenAIToolCalls 把 assistant 的工具调用块映射为 openai 的 tool_calls。
func toOpenAIToolCalls(blocks []types.ToolUseBlock) []openai.ToolCall {
	calls := make([]openai.ToolCall, 0, len(blocks))
	for _, b := range blocks {
		calls = append(calls, openai.ToolCall{
			ID:       b.ID,
			Type:     openai.ToolTypeFunction,
			Function: openai.FunctionCall{Name: b.Name, Arguments: string(b.Input)},
		})
	}
	return calls
}

// toOpenAITools 把工具 schema 映射为 openai function calling 声明。
func toOpenAITools(schemas []ToolSchema) []openai.Tool {
	tools := make([]openai.Tool, 0, len(schemas))
	for _, s := range schemas {
		tools = append(tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        s.Name,
				Description: s.Description,
				Parameters:  s.Parameters,
			},
		})
	}
	return tools
}

// mapRole 把 cogent 的消息角色映射为 OpenAI 兼容角色字符串。
func mapRole(r types.Role) string {
	switch r {
	case types.RoleSystem:
		return openai.ChatMessageRoleSystem
	case types.RoleAssistant:
		return openai.ChatMessageRoleAssistant
	case types.RoleTool:
		return openai.ChatMessageRoleTool
	default:
		return openai.ChatMessageRoleUser
	}
}

// toolCallBuf 累积流式分片到达的单个工具调用（id/name 来自首片，arguments 跨片拼接）。
type toolCallBuf struct {
	id   string          // 调用 ID
	name string          // 工具名
	args strings.Builder // arguments JSON 分片累积
}

// toolAcc 按 index 累积一次流式响应里的多个并行工具调用，保序刷出。
type toolAcc struct {
	bufs  map[int]*toolCallBuf // index → 累积缓冲
	order []int                // 首次出现顺序，保证刷出与模型请求一致
}

// newToolAcc 构造一个空的工具调用累积器。
func newToolAcc() *toolAcc {
	return &toolAcc{bufs: make(map[int]*toolCallBuf)}
}

// add 把一帧的 tool_calls 分片并入累积器；Index 在流式块中标识所属调用。
func (a *toolAcc) add(calls []openai.ToolCall) {
	for _, c := range calls {
		idx := 0
		if c.Index != nil {
			idx = *c.Index
		}
		b, ok := a.bufs[idx]
		if !ok {
			b = &toolCallBuf{}
			a.bufs[idx] = b
			a.order = append(a.order, idx)
		}
		if c.ID != "" {
			b.id = c.ID
		}
		if c.Function.Name != "" {
			b.name = c.Function.Name
		}
		b.args.WriteString(c.Function.Arguments)
	}
}

// flush 把已集齐的工具调用组装为 ToolUseBlock 逐个上抛并清空；返回 false 表示 ctx 已取消。
func (a *toolAcc) flush(ctx context.Context, out chan<- Delta) bool {
	for _, idx := range a.order {
		b := a.bufs[idx]
		if b.name == "" {
			continue
		}
		args := b.args.String()
		if strings.TrimSpace(args) == "" {
			args = "{}" // 无参工具调用规整为合法空对象
		}
		block := &types.ToolUseBlock{ID: b.id, Name: b.name, Input: json.RawMessage(args)}
		if !emit(ctx, out, Delta{ToolCall: block}) {
			return false
		}
	}
	a.bufs = make(map[int]*toolCallBuf)
	a.order = nil
	return true
}
