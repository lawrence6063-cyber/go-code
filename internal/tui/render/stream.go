package render

import "strings"

// StreamMarkdown 是有状态的流式 Markdown-lite 渲染器：按到达的文本块缓冲，
// 每凑齐完整行即输出其 ANSI 渲染结果，并跨块维护代码围栏（```）状态。
// 只前进、不回改光标，避免流式重绘撕裂。非并发安全，单轮回复内独占使用。
type StreamMarkdown struct {
	buf     strings.Builder // 尚未凑齐整行的尾部
	inFence bool            // 是否处于代码围栏内
}

// Write 追加一个文本块，返回其中已完成行（以换行结尾）的渲染输出；
// 未完成的行尾留待后续 Write 或 Flush。
func (s *StreamMarkdown) Write(chunk string) string {
	s.buf.WriteString(chunk)
	content := s.buf.String()
	idx := strings.LastIndexByte(content, '\n')
	if idx < 0 {
		return ""
	}
	complete := content[:idx+1]
	rest := content[idx+1:]
	s.buf.Reset()
	s.buf.WriteString(rest)
	return s.renderLines(complete)
}

// Flush 渲染并返回缓冲中剩余的不完整行，随后清空缓冲。
func (s *StreamMarkdown) Flush() string {
	if s.buf.Len() == 0 {
		return ""
	}
	line := s.buf.String()
	s.buf.Reset()
	return s.renderLine(line)
}

// renderLines 渲染以换行分隔的多行（complete 以换行结尾），逐行还原换行。
func (s *StreamMarkdown) renderLines(complete string) string {
	lines := strings.Split(complete, "\n")
	var b strings.Builder
	for i := 0; i < len(lines)-1; i++ {
		b.WriteString(s.renderLine(lines[i]))
		b.WriteByte('\n')
	}
	return b.String()
}

// renderLine 渲染单行并按需翻转围栏状态，与 RenderMarkdown 的逐行规则一致。
func (s *StreamMarkdown) renderLine(line string) string {
	switch {
	case strings.HasPrefix(strings.TrimSpace(line), "```"):
		s.inFence = !s.inFence
		return Faint(line)
	case s.inFence:
		return Cyan(line)
	case isHeading(line):
		return Bold(line)
	default:
		return renderInline(line)
	}
}
