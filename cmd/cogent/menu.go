package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/alaindong/cogent/internal/render"
)

// menuStatus 表示单选菜单处理一次按键后的状态流转。
type menuStatus int

const (
	menuContinue  menuStatus = iota // 继续等待按键
	menuConfirm                     // 确认选中项
	menuCancel                      // 取消（Esc/Ctrl-G）
	menuInterrupt                   // 中断（Ctrl-C）
)

// menuOutcome 表示一次 runMenu 交互的最终结果。
type menuOutcome int

const (
	menuOK          menuOutcome = iota // 用户确认了某选项
	menuCancelled                      // 用户取消（Esc/Ctrl-G）
	menuInterrupted                    // 用户中断（Ctrl-C）
	menuUnavailable                    // 无法进入 raw（非 TTY），应回退文本交互
)

// menuModel 是与 I/O 解耦的单选菜单状态机：维护选项与当前选中项，便于纯逻辑单测。
type menuModel struct {
	options []string
	sel     int
}

// newMenuModel 构造一个单选菜单模型（默认选中首项）。
func newMenuModel(options []string) *menuModel {
	return &menuModel{options: options}
}

// handleKey 处理单个按键并返回状态流转：↑↓ 循环移动，Enter 确认，Esc/Ctrl-G 取消，Ctrl-C 中断。
func (m *menuModel) handleKey(k keyEvent) menuStatus {
	switch k.typ {
	case keyUp:
		m.move(-1)
	case keyDown:
		m.move(1)
	case keyEnter:
		return menuConfirm
	case keyEsc, keyCtrlG:
		return menuCancel
	case keyCtrlC:
		return menuInterrupt
	default:
		// 其他按键忽略，保持菜单。
	}
	return menuContinue
}

// move 在选项范围内循环移动选中项。
func (m *menuModel) move(d int) {
	n := len(m.options)
	if n == 0 {
		return
	}
	m.sel = (m.sel + d + n) % n
}

// runMenu 在终端 f 上以 raw 模式渲染单选菜单并返回选中索引与结果；out 为渲染输出流。
// 无法进入 raw（非 TTY）时返回 menuUnavailable，调用方据此回退文本交互；
// 以 defer restore 保证退出恢复终端，Ctrl-C 亦干净返回。
func runMenu(ctx context.Context, f *os.File, out io.Writer, options []string) (int, menuOutcome) {
	if len(options) == 0 {
		return 0, menuUnavailable
	}
	restore, err := enterRaw(f.Fd())
	if err != nil {
		return 0, menuUnavailable
	}
	defer func() { _ = restore() }()

	m := newMenuModel(options)
	src := newRawInput(f)
	fmt.Fprint(out, "\x1b7") // DECSC：保存菜单起点
	for {
		renderMenu(out, m)
		k, derr := decodeKey(ctx, src)
		if derr != nil {
			finishMenu(out)
			return 0, menuInterrupted
		}
		switch m.handleKey(k) {
		case menuConfirm:
			finishMenu(out)
			return m.sel, menuOK
		case menuCancel:
			finishMenu(out)
			return 0, menuCancelled
		case menuInterrupt:
			finishMenu(out)
			return 0, menuInterrupted
		}
	}
}

// renderMenu 复位到起点后重绘菜单，高亮当前选中项。
func renderMenu(w io.Writer, m *menuModel) {
	var b strings.Builder
	b.WriteString("\x1b8\x1b[0J") // DECRC + 清除到屏幕末尾
	for i, opt := range m.options {
		if i > 0 {
			b.WriteString("\r\n")
		}
		if i == m.sel {
			b.WriteString(render.Highlight("> " + opt))
			continue
		}
		b.WriteString("  " + opt)
	}
	_, _ = io.WriteString(w, b.String())
}

// finishMenu 收尾：复位并清除菜单渲染区，使后续输出从干净位置开始。
func finishMenu(w io.Writer) {
	_, _ = io.WriteString(w, "\x1b8\x1b[0J")
}
