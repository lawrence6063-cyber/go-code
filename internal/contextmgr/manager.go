// Package contextmgr 负责上下文窗口计算与自动压缩（Context Engineering）。
// 长任务历史会撑爆模型窗口；本包在阈值触发时压缩历史并重注入关键状态，
// 压缩切点避开 tool_use/tool_result 配对，连续失败达阈值后熔断停止徒劳压缩。
package contextmgr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/alaindong/cogent/internal/llm"
	"github.com/alaindong/cogent/internal/types"
)

// ErrCompactGiveUp 表示压缩连续失败达阈值后触发熔断，停止再压缩。
var ErrCompactGiveUp = errors.New("compact circuit open")

// 上下文窗口与压缩相关默认常量（窗口数字以 DeepSeek 128K 为基准，均可经 env 覆盖）。
const (
	ContextWindowDefault   = 131072 // 总窗口（token）：DeepSeek 128K
	ReservedForSummary     = 8000   // 为压缩摘要预留的输出 token（同时作摘要调用的 MaxTokens 上限，对齐 DeepSeek 8K 出参上限）
	AutoCompactBufferToken = 8000   // 提前触发压缩的缓冲；128K 大窗口下加大以更早规避溢出
	MaxConsecutiveFailures = 3      // 压缩连续失败熔断阈值
	KeepRecentTokens       = 24000  // 压缩时从尾部保留的最近原文 token 下限；随窗口翻倍以多留近期上下文
	avgCharsPerToken       = 4      // token 粗估：runes / 4
)

// summaryTemperature 是压缩摘要调用的采样温度；取低温保证状态摘要稳定、可复现。
const summaryTemperature = 0.2

// 窗口参数对应的环境变量名。
const (
	envWindow  = "COGENT_CONTEXT_WINDOW"  // 覆盖总窗口
	envReserve = "COGENT_CONTEXT_RESERVE" // 覆盖摘要预留
	envBuffer  = "COGENT_CONTEXT_BUFFER"  // 覆盖触发缓冲
	envKeep    = "COGENT_CONTEXT_KEEP"    // 覆盖尾部保留下限
)

// summaryPrompt 指导 LLM 把被丢弃的历史压成可继续工作的状态摘要（含重注入要点）。
const summaryPrompt = "You are compacting an autonomous coding agent's conversation history to save context. " +
	"Summarize the following earlier messages into a concise but complete state note so the agent can continue seamlessly. " +
	"Preserve: the user's overall goal, files currently being read or edited, the in-progress plan and remaining TODOs, " +
	"key decisions AND the reasons behind them, important findings, and any unresolved problems or errors encountered. " +
	"Omit chit-chat. Output only the summary text."

// Manager 负责上下文窗口计算与自动压缩，持有窗口配置与连续失败计数。
type Manager struct {
	window     int // 总窗口
	reserved   int // 摘要预留
	buffer     int // 触发缓冲
	keepRecent int // 尾部保留下限
	maxFail    int // 熔断阈值
	failures   int // 当前连续失败计数
}

// New 构造一个上下文管理器；窗口参数默认按 DeepSeek 128K，可经环境变量覆盖。
func New() *Manager {
	return &Manager{
		window:     envInt(envWindow, ContextWindowDefault),
		reserved:   envInt(envReserve, ReservedForSummary),
		buffer:     envInt(envBuffer, AutoCompactBufferToken),
		keepRecent: envInt(envKeep, KeepRecentTokens),
		maxFail:    MaxConsecutiveFailures,
	}
}

// effectiveWindow 返回有效上下文窗口（总窗口减去为摘要预留的输出 token）。
func (m *Manager) effectiveWindow() int {
	return m.window - m.reserved
}

// ShouldCompact 依据已用 token 与有效窗口判断是否需要压缩；熔断后恒返回 false。
func (m *Manager) ShouldCompact(usedTokens int, _ string) bool {
	if m.failures >= m.maxFail {
		return false
	}
	return usedTokens+m.buffer >= m.effectiveWindow()
}

// Compact 压缩历史消息：保留尾部最近消息、平移切点保 function calling 配对、
// 对丢弃段调 LLM 生成摘要后重建为 [system]+[摘要]+[尾部]。model 为摘要调用使用的模型名
// （必须与主循环一致，否则部分提供方会因空模型名报 400）。
// 失败累加计数，达熔断阈值返回 ErrCompactGiveUp；任何失败都返回原消息，绝不丢历史。
func (m *Manager) Compact(ctx context.Context, msgs []types.Message, llmc llm.Client, model string) ([]types.Message, error) {
	if len(msgs) < 2 {
		return msgs, nil // 仅有系统提示，无可压缩历史
	}
	cut := adjustForPairing(msgs, m.findCutPoint(msgs))
	if cut <= 1 {
		return msgs, nil // 尾部已覆盖全部历史，无需压缩
	}
	summary, err := summarize(ctx, llmc, msgs[1:cut], m.reserved, model)
	if err != nil {
		return m.recordFailure(msgs, err)
	}
	m.failures = 0
	return rebuild(msgs, cut, summary), nil
}

// recordFailure 累加失败计数并返回原消息；达熔断阈值时返回 ErrCompactGiveUp。
func (m *Manager) recordFailure(msgs []types.Message, err error) ([]types.Message, error) {
	m.failures++
	if m.failures >= m.maxFail {
		return msgs, ErrCompactGiveUp
	}
	return msgs, fmt.Errorf("compact: %w", err)
}

// findCutPoint 从尾部累加 token 找到保留 keepRecent 的切点：msgs[cut:] 为保留段。
// 切点不会越过系统提示（cut >= 1）。
func (m *Manager) findCutPoint(msgs []types.Message) int {
	acc := 0
	for i := len(msgs) - 1; i >= 1; i-- {
		acc += EstimateMessage(msgs[i])
		if acc >= m.keepRecent {
			return i
		}
	}
	return 1
}

// adjustForPairing 把切点向头部平移，避免保留段以孤立的 tool_result 开头：
// 若 msgs[cut] 为 RoleTool，则其配对的 assistant(tool_calls) 在更早处，需一并纳入保留段。
func adjustForPairing(msgs []types.Message, cut int) int {
	for cut > 1 && msgs[cut].Role == types.RoleTool {
		cut--
	}
	return cut
}

// summarize 把被丢弃段序列化后调 LLM 生成单条状态摘要；收齐全部文本增量。
// maxTokens 用摘要预留 token 限制输出长度，与有效窗口的预留形成闭环；低温保证稳定。
// model 必须显式传入并与主循环一致——某些提供方（如 DeepSeek）对空模型名返回 400。
func summarize(ctx context.Context, llmc llm.Client, discarded []types.Message, maxTokens int, model string) (string, error) {
	req := llm.Request{
		Messages: []types.Message{
			{Role: types.RoleSystem, Text: summaryPrompt},
			{Role: types.RoleUser, Text: serialize(discarded)},
		},
		Model:       model,
		Temperature: summaryTemperature,
		MaxTokens:   maxTokens,
	}
	deltas, err := llmc.Stream(ctx, req)
	if err != nil {
		return "", fmt.Errorf("summarize stream: %w", err)
	}
	var sb strings.Builder
	for d := range deltas {
		if d.Err != nil {
			return "", fmt.Errorf("summarize delta: %w", d.Err)
		}
		sb.WriteString(d.Text)
	}
	if strings.TrimSpace(sb.String()) == "" {
		return "", errors.New("empty summary")
	}
	return sb.String(), nil
}

// rebuild 用 [系统提示] + [摘要(RoleUser)] + [尾部最近消息] 重建消息列表。
func rebuild(msgs []types.Message, cut int, summary string) []types.Message {
	out := make([]types.Message, 0, len(msgs)-cut+2)
	out = append(out, msgs[0])
	out = append(out, types.Message{
		Role: types.RoleUser,
		Text: "[Earlier conversation summary]\n" + summary,
	})
	out = append(out, msgs[cut:]...)
	return out
}

// serialize 把一段消息拼成可供摘要的纯文本（含角色与工具调用名）。
func serialize(msgs []types.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		sb.WriteString(string(m.Role))
		sb.WriteString(": ")
		sb.WriteString(m.Text)
		for _, tc := range m.ToolCalls {
			sb.WriteString("\n[tool_call ")
			sb.WriteString(tc.Name)
			sb.WriteString("] ")
			sb.WriteString(string(tc.Input))
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// EstimateTokens 粗估一组消息的 token 数（runes/4），用于无真实 usage 时校准。
func EstimateTokens(msgs []types.Message) int {
	total := 0
	for _, m := range msgs {
		total += EstimateMessage(m)
	}
	return total
}

// EstimateMessage 粗估单条消息的 token 数（文本与工具调用入参均计入）。
func EstimateMessage(m types.Message) int {
	chars := len([]rune(m.Text))
	for _, tc := range m.ToolCalls {
		chars += len([]rune(string(tc.Input)))
	}
	return chars / avgCharsPerToken
}

// envInt 读取整型环境变量；缺失或非法时回退默认值。
func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
