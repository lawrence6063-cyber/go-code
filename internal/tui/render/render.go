// Package render 提供终端富文本渲染的纯函数基座：ANSI 样式、Markdown-lite 着色、
// unified diff 着色，以及 CJK 双宽感知的显示宽度与截断。
//
// 本包只依赖标准库、无副作用，便于单测；是否启用富渲染由调用方决定
// （非 TTY 场景不应调用本包的着色函数，以免污染管道输出）。
// 约定：DisplayWidth/Truncate/RenderMarkdown 的入参为「不含 ANSI 转义」的纯文本；
// 若需先测宽再着色，请先算宽度、再用样式 helper 包裹。
package render

import (
	"strings"
	"unicode"
)

// ANSI SGR（Select Graphic Rendition）序列常量。
const (
	ansiReset     = "\x1b[0m"  // 复位所有属性
	ansiBold      = "\x1b[1m"  // 加粗
	ansiFaint     = "\x1b[2m"  // 变暗
	ansiReverse   = "\x1b[7m"  // 反显（前景/背景互换）
	ansiFgRed     = "\x1b[31m" // 红
	ansiFgGreen   = "\x1b[32m" // 绿
	ansiFgYellow  = "\x1b[33m" // 黄
	ansiFgCyan    = "\x1b[36m" // 青
	ansiFgBrBlack = "\x1b[90m" // 亮黑（灰）
)

// Bold 以加粗样式包裹 s（空串原样返回）。
func Bold(s string) string { return stylize(s, ansiBold) }

// Faint 以变暗样式包裹 s，用于次要信息（如上下文行、提示）。
func Faint(s string) string { return stylize(s, ansiFaint) }

// Green 以绿色前景包裹 s。
func Green(s string) string { return stylize(s, ansiFgGreen) }

// Red 以红色前景包裹 s。
func Red(s string) string { return stylize(s, ansiFgRed) }

// Yellow 以黄色前景包裹 s。
func Yellow(s string) string { return stylize(s, ansiFgYellow) }

// Cyan 以青色前景包裹 s。
func Cyan(s string) string { return stylize(s, ansiFgCyan) }

// Gray 以亮黑（灰）前景包裹 s。
func Gray(s string) string { return stylize(s, ansiFgBrBlack) }

// Highlight 以反显样式包裹 s，用于菜单选中项或行内高亮。
func Highlight(s string) string { return stylize(s, ansiReverse) }

// stylize 用 code 包裹 s 并在末尾复位；s 为空时原样返回（避免产生空的着色块）。
func stylize(s, code string) string {
	if s == "" {
		return s
	}
	return code + s + ansiReset
}

// headingPrefixMax 是 Markdown 标题识别的最大 '#' 前缀数（# 到 ######）。
const headingPrefixMax = 6

// RenderMarkdown 把 Markdown-lite 文本渲染为带 ANSI 样式的字符串：
// 支持 ATX 标题（# 到 ######）加粗、代码围栏（```）整块变暗、行内加粗（**text**）
// 与行内代码（`code`）着色。不识别的语法原样保留。为可预期，逐行处理并保留换行。
func RenderMarkdown(s string) string {
	lines := strings.Split(s, "\n")
	var b strings.Builder
	b.Grow(len(s) + len(s)/4)
	inFence := false
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		switch {
		case strings.HasPrefix(strings.TrimSpace(line), "```"):
			inFence = !inFence
			b.WriteString(Faint(line))
		case inFence:
			b.WriteString(Cyan(line))
		case isHeading(line):
			b.WriteString(Bold(line))
		default:
			b.WriteString(renderInline(line))
		}
	}
	return b.String()
}

// isHeading 判断 line 是否为 ATX 标题：1..headingPrefixMax 个 '#' 后跟空格。
func isHeading(line string) bool {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	return n >= 1 && n <= headingPrefixMax && n < len(line) && line[n] == ' '
}

// renderInline 对单行做行内替换：`**bold**` 加粗、“ `code` “ 着色。
// 单遍扫描按 rune 处理，未闭合的标记原样输出。
func renderInline(line string) string {
	rs := []rune(line)
	var b strings.Builder
	for i := 0; i < len(rs); {
		if i+1 < len(rs) && rs[i] == '*' && rs[i+1] == '*' {
			if end := findClose(rs, i+2, "**"); end >= 0 {
				b.WriteString(Bold(string(rs[i+2 : end])))
				i = end + 2
				continue
			}
		}
		if rs[i] == '`' {
			if end := findClose(rs, i+1, "`"); end >= 0 {
				b.WriteString(Cyan(string(rs[i+1 : end])))
				i = end + 1
				continue
			}
		}
		b.WriteRune(rs[i])
		i++
	}
	return b.String()
}

// findClose 从 rs[from:] 起查找闭合标记 marker 的起始 rune 下标；未找到返回 -1。
func findClose(rs []rune, from int, marker string) int {
	m := []rune(marker)
	for i := from; i+len(m) <= len(rs); i++ {
		if matchAt(rs, i, m) {
			return i
		}
	}
	return -1
}

// matchAt 判断 rs 从下标 i 起是否与 m 完全相等。
func matchAt(rs []rune, i int, m []rune) bool {
	for j := range m {
		if rs[i+j] != m[j] {
			return false
		}
	}
	return true
}

// DisplayWidth 返回 s 在等宽终端下占用的列数：控制符与组合字符计 0，
// 东亚全/宽字符计 2，其余计 1。入参应为不含 ANSI 转义的纯文本。
func DisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

// Truncate 按显示列宽把 s 截断到至多 cols 列：超出时以 '…' 结尾（省略号占 1 列）。
// cols<=0 时返回空串；不会在多字节 rune 中间截断。
func Truncate(s string, cols int) string {
	if cols <= 0 {
		return ""
	}
	if DisplayWidth(s) <= cols {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := runeWidth(r)
		if w+rw > cols-1 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	b.WriteRune('…')
	return b.String()
}

// runeWidth 返回单个 rune 的显示列宽（0/1/2）。
func runeWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case unicode.IsControl(r):
		return 0
	case unicode.In(r, unicode.Mn, unicode.Me):
		return 0
	case isWide(r):
		return 2
	default:
		return 1
	}
}

// isWide 判断 r 是否为东亚全角/宽字符（占 2 列）。覆盖常见 CJK、假名、
// 谚文、全角标点与 CJK 扩展平面，非精确 UAX #11 但对终端对齐足够。
func isWide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F: // Hangul Jamo
		return true
	case r >= 0x2E80 && r <= 0x303E: // CJK 部首补充 .. CJK 符号
		return true
	case r >= 0x3041 && r <= 0x33FF: // 平/片假名 .. CJK 兼容
		return true
	case r >= 0x3400 && r <= 0x4DBF: // CJK 扩展 A
		return true
	case r >= 0x4E00 && r <= 0x9FFF: // CJK 统一表意
		return true
	case r >= 0xA000 && r <= 0xA4CF: // 彝文
		return true
	case r >= 0xAC00 && r <= 0xD7A3: // 谚文音节
		return true
	case r >= 0xF900 && r <= 0xFAFF: // CJK 兼容表意
		return true
	case r >= 0xFE30 && r <= 0xFE4F: // CJK 兼容形式
		return true
	case r >= 0xFF00 && r <= 0xFF60: // 全角 ASCII
		return true
	case r >= 0xFFE0 && r <= 0xFFE6: // 全角符号
		return true
	case r >= 0x1F300 && r <= 0x1FAFF: // 表情符号（多为宽）
		return true
	case r >= 0x20000 && r <= 0x3FFFD: // CJK 扩展 B+
		return true
	default:
		return false
	}
}
