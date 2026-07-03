package tui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"unicode"

	"github.com/alaindong/cogent/internal/tui/completion"
	"github.com/alaindong/cogent/internal/tui/history"
)

// lineEditor 是 raw 模式交互式行编辑器：管理提示符之后的可编辑区与其下方的补全下拉。
// 从 in（终端 stdin）读键，向 out（stdout，提示符所在流）写渲染，保持光标状态一致，
// 并跨行共享输入历史。
type lineEditor struct {
	in                 *os.File
	out                io.Writer
	provider           completion.Provider
	history            *history.Store
	promptWidth        int // 提示符 "you> " 的显示列宽，drawLine 重绘时跳过此前缀
	drawnDropdownLines int // 上次 drawLine 渲染的下拉行数（用于相对回退清除）
}

// defaultPromptWidth 是 "you> " 提示符的显示列宽。
const defaultPromptWidth = 4

// newLineEditor 构造绑定到终端 f、以 workRoot 为候选来源与历史落盘目录的行编辑器。
func newLineEditor(f *os.File, workRoot string) *lineEditor {
	return &lineEditor{
		in:          f,
		out:         os.Stdout,
		provider:    completion.NewProvider(workRoot),
		history:     history.New(workRoot),
		promptWidth: defaultPromptWidth,
	}
}

// readLine 进入 raw 模式读取一行，返回三态：
//   - ok=true：成功读到整行（line 有效）。
//   - ok=false, usable=true：用户主动结束（Ctrl-C/空行 Ctrl-D）或 ctx 取消——调用方应据此退出。
//   - ok=false, usable=false：raw 交互不可用（无法进入 raw，或首次读键即底层 I/O 错误）——
//     调用方应回退到逐行读取，而非退出。此三态区分是为了不让"环境不支持 raw"被误判成 EOF 而秒退。
//
// 调用方需已打印提示符；编辑器从提示符末尾开始绘制可编辑区及下拉。
func (le *lineEditor) readLine(ctx context.Context) (line string, ok bool, usable bool) {
	restore, err := enterRaw(le.in.Fd())
	if err != nil {
		slog.Debug("lineeditor: enter raw failed", "err", err)
		return "", false, false // 进不去 raw：不可用，请回退
	}
	defer func() { _ = restore() }()

	src := newRawInput(le.in)
	core := newEditorCore(ctx, le.provider)
	core.history = le.history // 注入共享历史，跨行保留
	le.drawnDropdownLines = 0
	readAny := false
	for {
		le.drawLine(core)
		k, derr := decodeKey(ctx, src)
		if derr != nil {
			le.finishLine(core)
			if !readAny && ctx.Err() == nil {
				return "", false, false
			}
			return "", false, true
		}
		readAny = true
		switch core.handleKey(k) {
		case statusSubmit:
			le.finishLine(core)
			le.history.Append(string(core.line))
			return string(core.line), true, true
		case statusInterrupt, statusEOF:
			le.finishLine(core)
			return "", false, true
		}
	}
}

// drawLine 重绘可编辑区与下拉。使用相对光标移动（而非 DECSC/DECRC）以正确处理终端滚动。
// 策略：上移回退到上次渲染的起点 → 跳过提示符 → 清到屏末 → 重绘用户输入 + 下拉。
func (le *lineEditor) drawLine(core *editorCore) {
	var b strings.Builder
	// 1) 回退到输入行：如果上次画了 N 行下拉/提示，光标在末行，需要上移回到输入行。
	if le.drawnDropdownLines > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", le.drawnDropdownLines)
	}
	// 2) 回行首 → 跳过提示符 → 清提示符之后到屏幕末尾（保留 "you> "）。
	b.WriteString("\r")
	if le.promptWidth > 0 {
		fmt.Fprintf(&b, "\x1b[%dC", le.promptWidth)
	}
	b.WriteString("\x1b[0J")

	// 3) 绘制内容。
	dropLines := 0
	if core.mode == modeSearch {
		fmt.Fprintf(&b, "(reverse-i-search)`%s': %s", string(core.query), string(core.line))
	} else {
		b.WriteString(string(core.line))
		if core.active && len(core.sugg) > 0 {
			dropLines = le.renderDropdown(&b, core)
		}
		if core.ctrlCPending {
			b.WriteString("\r\n\x1b[2K")
			b.WriteString("\x1b[2m(press Ctrl-C again to exit)\x1b[0m")
			dropLines++
		}
	}
	le.drawnDropdownLines = dropLines

	// 4) 把光标放回输入行的正确列位置（按显示宽度）。
	if le.drawnDropdownLines > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", le.drawnDropdownLines)
	}
	col := le.promptWidth + displayWidth(core.line, core.cursor)
	b.WriteString("\r")
	if col > 0 {
		fmt.Fprintf(&b, "\x1b[%dC", col)
	}

	_, _ = io.WriteString(le.out, b.String())
}

// renderDropdown 在当前行下方渲染候选窗口，高亮选中项；返回渲染了多少行。
func (le *lineEditor) renderDropdown(b *strings.Builder, core *editorCore) int {
	end := core.offset + maxVisibleSuggest
	if end > len(core.sugg) {
		end = len(core.sugg)
	}
	lines := 0
	for i := core.offset; i < end; i++ {
		b.WriteString("\r\n")
		b.WriteString("\x1b[2K") // 清整行（防止残留）
		if i == core.sel {
			b.WriteString("\x1b[7m> ")
			b.WriteString(core.sugg[i])
			b.WriteString("\x1b[0m")
		} else {
			b.WriteString("  ")
			b.WriteString(core.sugg[i])
		}
		lines++
	}
	return lines
}

// finishLine 收尾：清除下拉，回显最终行并换行，使后续输出从新行开始。
func (le *lineEditor) finishLine(core *editorCore) {
	var b strings.Builder
	// 回退到输入行并清除下拉。
	if le.drawnDropdownLines > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", le.drawnDropdownLines)
	}
	// 跳过提示符再清，保留 "you> "。
	b.WriteString("\r")
	if le.promptWidth > 0 {
		fmt.Fprintf(&b, "\x1b[%dC", le.promptWidth)
	}
	b.WriteString("\x1b[0J")
	b.WriteString(string(core.line))
	b.WriteString("\r\n")
	le.drawnDropdownLines = 0
	_, _ = io.WriteString(le.out, b.String())
}

// ─── CJK 宽字符显示宽度 ───

// displayWidth 计算 line[:pos] 在终端的显示列宽（CJK 双宽感知）。
func displayWidth(line []rune, pos int) int {
	w := 0
	for i := 0; i < pos && i < len(line); i++ {
		w += runeDisplayWidth(line[i])
	}
	return w
}

// runeDisplayWidth 返回单个 rune 在等宽终端占用的列数（0/1/2）。
func runeDisplayWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case unicode.IsControl(r):
		return 0
	case unicode.In(r, unicode.Mn, unicode.Me):
		return 0
	case isWideRune(r):
		return 2
	default:
		return 1
	}
}

// isWideRune 判断 r 是否为东亚全角/宽字符（占 2 列）。覆盖常见 CJK、假名、
// 谚文、全角标点与 CJK 扩展平面。
func isWideRune(r rune) bool {
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
