package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/alaindong/cogent/internal/completion"
	"github.com/alaindong/cogent/internal/history"
)

// 补全下拉的规模上限：maxSuggest 为拉取候选数，maxVisibleSuggest 为同屏可见行数。
const (
	maxSuggest        = 15
	maxVisibleSuggest = 5
)

// editorStatus 表示单次按键处理后的编辑器状态流转。
type editorStatus int

const (
	statusContinue  editorStatus = iota // 继续编辑
	statusSubmit                        // 提交整行
	statusInterrupt                     // 中断（Ctrl-C）
	statusEOF                           // 输入结束（空行 Ctrl-D）
)

// keyType 是解码后的按键类别。
type keyType int

const (
	keyRune keyType = iota // 可打印字符（含多字节 rune）
	keyEnter
	keyTab
	keyEsc
	keyBackspace
	keyUp
	keyDown
	keyLeft
	keyRight
	keyHome
	keyEnd
	keyCtrlC
	keyCtrlD
	keyCtrlU
	keyCtrlR
	keyCtrlG
	keyUnknown
)

// keyEvent 是一次按键事件；typ==keyRune 时 r 为对应字符。
type keyEvent struct {
	typ keyType
	r   rune
}

func ev(t keyType) keyEvent     { return keyEvent{typ: t} }
func runeEvent(r rune) keyEvent { return keyEvent{typ: keyRune, r: r} }

// completionKind 标识当前下拉候选的来源类别。
type completionKind int

const (
	kindFile    completionKind = iota // @ 文件路径补全
	kindCommand                       // / 斜杠命令补全
)

// editorMode 标识行编辑器当前所处的输入子模式。
type editorMode int

const (
	modeNormal editorMode = iota // 常规编辑（含补全/历史导航）
	modeSearch                   // reverse-i-search 反向增量历史搜索
)

// histNone 表示当前未处于历史浏览态（在编辑新行）。
const histNone = -1

// ctrlCTimeout 是双击 Ctrl-C 退出的时间窗口。
const ctrlCTimeout = 800 * time.Millisecond

// editorCore 是与终端 I/O 解耦的行编辑状态机：维护行缓冲、光标、补全下拉（@ 文件/ / 命令）、
// 历史导航与反向搜索状态，便于纯逻辑单测（喂 keyEvent 序列断言状态）。
type editorCore struct {
	ctx          context.Context
	provider     completion.Provider
	cmds         completion.CommandProvider
	history      *history.Store
	line         []rune
	cursor       int
	sugg         []string
	sel          int
	active       bool
	offset       int              // 下拉可见窗口起点
	kind         completionKind   // 当前候选来源
	mode         editorMode       // 当前输入子模式
	histIdx      int              // 历史浏览位置（histNone 表示未浏览）
	stash        []rune           // 进入历史浏览/搜索前暂存的编辑行
	query        []rune           // 搜索模式的查询串
	searchIdx    int              // 当前搜索命中的历史索引（自末尾计）
	ctrlCPending bool             // 是否处于双击 Ctrl-C 退出等待态
	lastCtrlCAt  time.Time        // 上次空行 Ctrl-C 的时间戳
	nowFunc      func() time.Time // 可注入的时钟（测试用，nil 时用 time.Now）
}

// newEditorCore 构造一个空的行编辑状态机（默认启用内建斜杠命令补全与纯内存历史）。
// 调用方可在返回后注入共享的 history.Store 以跨行保留历史。
func newEditorCore(ctx context.Context, p completion.Provider) *editorCore {
	return &editorCore{
		ctx:      ctx,
		provider: p,
		cmds:     completion.NewCommandProvider(),
		history:  history.NewMemory(),
		line:     make([]rune, 0, 64),
		histIdx:  histNone,
	}
}

// now 返回当前时间（支持测试注入）。
func (e *editorCore) now() time.Time {
	if e.nowFunc != nil {
		return e.nowFunc()
	}
	return time.Now()
}

// handleKey 按当前子模式分派按键处理。
func (e *editorCore) handleKey(k keyEvent) editorStatus {
	if e.mode == modeSearch {
		return e.handleSearchKey(k)
	}
	return e.handleNormalKey(k)
}

// handleNormalKey 处理常规编辑模式的按键；具体动作委托给各 helper。
func (e *editorCore) handleNormalKey(k keyEvent) editorStatus {
	// 任何非 Ctrl-C 按键到来时，惰性清除双击退出等待态。
	if k.typ != keyCtrlC {
		e.ctrlCPending = false
	}

	switch k.typ {
	case keyCtrlC:
		// 有文本或下拉激活 → 清行 + 关闭下拉 + 清除 pending。
		if len(e.line) > 0 || e.active {
			e.line = e.line[:0]
			e.cursor = 0
			e.histIdx = histNone
			e.closeDropdown()
			e.ctrlCPending = false
			return statusContinue
		}
		// 空行：双击退出机制。
		now := e.now()
		if e.ctrlCPending && now.Sub(e.lastCtrlCAt) <= ctrlCTimeout {
			// 800ms 内二次 → 真正退出。
			e.ctrlCPending = false
			return statusInterrupt
		}
		// 首次（或已超时）→ 设 pending，等下次 drawLine 显示提示。
		e.ctrlCPending = true
		e.lastCtrlCAt = now
		return statusContinue
	case keyCtrlD:
		if len(e.line) == 0 {
			return statusEOF
		}
	case keyCtrlR:
		e.enterSearch()
	case keyEnter:
		if e.active && len(e.sugg) > 0 {
			e.accept()
			break
		}
		return statusSubmit
	case keyTab:
		if e.active && len(e.sugg) > 0 {
			e.accept()
		}
	case keyEsc:
		e.closeDropdown()
	case keyUp:
		e.moveUp()
	case keyDown:
		e.moveDown()
	case keyLeft:
		e.moveCursor(-1)
		e.refresh()
	case keyRight:
		e.moveCursor(1)
		e.refresh()
	case keyHome:
		e.cursor = 0
		e.refresh()
	case keyEnd:
		e.cursor = len(e.line)
		e.refresh()
	case keyCtrlU:
		e.line, e.cursor, e.histIdx = e.line[:0], 0, histNone
		e.refresh()
	case keyBackspace:
		e.backspace()
		e.histIdx = histNone
		e.refresh()
	case keyRune:
		e.insert(k.r)
		e.histIdx = histNone
		e.refresh()
	}
	return statusContinue
}

// moveUp 在下拉激活时上移候选，否则回溯到更早的历史。
func (e *editorCore) moveUp() {
	if e.active {
		e.moveSelection(-1)
		return
	}
	e.historyPrev()
}

// moveDown 在下拉激活时下移候选，否则前进到更近的历史（或回到暂存编辑行）。
func (e *editorCore) moveDown() {
	if e.active {
		e.moveSelection(1)
		return
	}
	e.historyNext()
}

// insert 在光标处插入一个字符并右移光标。
func (e *editorCore) insert(r rune) {
	e.line = append(e.line, 0)
	copy(e.line[e.cursor+1:], e.line[e.cursor:])
	e.line[e.cursor] = r
	e.cursor++
}

// backspace 删除光标前一个字符。
func (e *editorCore) backspace() {
	if e.cursor == 0 {
		return
	}
	e.line = append(e.line[:e.cursor-1], e.line[e.cursor:]...)
	e.cursor--
}

// moveCursor 在合法区间内移动光标。
func (e *editorCore) moveCursor(d int) {
	e.cursor += d
	if e.cursor < 0 {
		e.cursor = 0
	}
	if e.cursor > len(e.line) {
		e.cursor = len(e.line)
	}
}

// moveSelection 在下拉激活时循环移动选中项并调整可见窗口。
func (e *editorCore) moveSelection(d int) {
	if !e.active || len(e.sugg) == 0 {
		return
	}
	e.sel = (e.sel + d + len(e.sugg)) % len(e.sugg)
	e.adjustWindow()
}

// adjustWindow 保证当前选中项落在可见窗口内。
func (e *editorCore) adjustWindow() {
	if e.sel < e.offset {
		e.offset = e.sel
	}
	if e.sel >= e.offset+maxVisibleSuggest {
		e.offset = e.sel - maxVisibleSuggest + 1
	}
}

// closeDropdown 关闭补全下拉并清空候选状态。
func (e *editorCore) closeDropdown() {
	e.active, e.sugg, e.sel, e.offset = false, nil, 0, 0
}

// accept 把当前选中候选写回行缓冲，随后关闭下拉；按候选来源分派写回逻辑。
func (e *editorCore) accept() {
	if len(e.sugg) == 0 {
		return
	}
	if e.kind == kindCommand {
		e.line, e.cursor = completion.ApplySlashChoice(e.line, e.sugg[e.sel])
		e.closeDropdown()
		return
	}
	tok := completion.ParseAtToken(e.line, e.cursor)
	if !tok.Active {
		return
	}
	e.line, e.cursor = completion.ApplyChoice(e.line, e.cursor, tok.Start, e.sugg[e.sel])
	e.closeDropdown()
}

// refresh 依据当前触发符重算候选：行首 / 优先命令补全，其次 @ 文件补全，都不满足则关闭下拉。
func (e *editorCore) refresh() {
	if tok := completion.ParseSlashToken(e.line, e.cursor); tok.Active {
		e.sugg = commandNames(e.cmds.Filter(tok.Partial, maxSuggest))
		e.kind = kindCommand
		e.settleDropdown()
		return
	}
	tok := completion.ParseAtToken(e.line, e.cursor)
	if !tok.Active {
		e.closeDropdown()
		return
	}
	e.sugg = e.provider.Filter(e.ctx, tok.Partial, maxSuggest)
	e.kind = kindFile
	e.settleDropdown()
}

// settleDropdown 依据最新候选修正激活态、选中项与可见窗口（refresh 各分支共用）。
func (e *editorCore) settleDropdown() {
	e.active = len(e.sugg) > 0
	if e.sel >= len(e.sugg) {
		e.sel = 0
	}
	if !e.active {
		e.offset = 0
	}
	e.adjustWindow()
}

// commandNames 抽取命令候选的名称列表（供下拉渲染与写回）。
func commandNames(cmds []completion.Command) []string {
	out := make([]string, len(cmds))
	for i := range cmds {
		out[i] = cmds[i].Name
	}
	return out
}

// historyPrev 回溯到更早一条历史；首次回溯前暂存当前编辑行。
func (e *editorCore) historyPrev() {
	if e.history == nil || e.history.Len() == 0 {
		return
	}
	if e.histIdx == histNone {
		e.stash = append([]rune(nil), e.line...)
	}
	if line, ok := e.history.At(e.histIdx + 1); ok {
		e.histIdx++
		e.setLine(line)
	}
}

// historyNext 前进到更近一条历史；越过最近一条后回到暂存的编辑行。
func (e *editorCore) historyNext() {
	if e.histIdx == histNone {
		return
	}
	e.histIdx--
	if e.histIdx == histNone {
		e.setLine(string(e.stash))
		return
	}
	if line, ok := e.history.At(e.histIdx); ok {
		e.setLine(line)
	}
}

// setLine 用 s 覆盖行缓冲、把光标置于末尾并重算补全。
func (e *editorCore) setLine(s string) {
	e.line = []rune(s)
	e.cursor = len(e.line)
	e.refresh()
}

// enterSearch 进入 reverse-i-search 子模式：暂存当前行、清空查询与下拉。
func (e *editorCore) enterSearch() {
	e.mode = modeSearch
	e.query = e.query[:0]
	e.searchIdx = 0
	e.histIdx = histNone
	e.stash = append([]rune(nil), e.line...)
	e.closeDropdown()
}

// handleSearchKey 处理搜索子模式的按键：键入即增量匹配，Ctrl-R 跳更早，Enter 落入行缓冲，
// Ctrl-G/Esc 取消并恢复原行。
func (e *editorCore) handleSearchKey(k keyEvent) editorStatus {
	switch k.typ {
	case keyCtrlC:
		return statusInterrupt
	case keyCtrlG, keyEsc:
		e.cancelSearch()
	case keyEnter:
		e.acceptSearch()
	case keyCtrlR:
		e.runSearch(e.searchIdx + 1)
	case keyBackspace:
		if len(e.query) > 0 {
			e.query = e.query[:len(e.query)-1]
		}
		e.runSearch(0)
	case keyRune:
		e.query = append(e.query, k.r)
		e.runSearch(0)
	default:
		// 其他按键在搜索模式下忽略，保持子模式。
	}
	return statusContinue
}

// runSearch 从自末尾第 from 条起向更早查找命中并预览到行缓冲；未命中则保持当前预览。
func (e *editorCore) runSearch(from int) {
	line, idx, ok := e.history.SearchBackward(string(e.query), from)
	if !ok {
		return
	}
	e.searchIdx = idx
	e.line = []rune(line)
	e.cursor = len(e.line)
}

// cancelSearch 放弃搜索并恢复进入前的编辑行。
func (e *editorCore) cancelSearch() {
	e.line = append([]rune(nil), e.stash...)
	e.cursor = len(e.line)
	e.mode = modeNormal
	e.query = e.query[:0]
	e.closeDropdown()
}

// acceptSearch 采纳当前命中行、退出搜索并重算补全。
func (e *editorCore) acceptSearch() {
	e.mode = modeNormal
	e.query = e.query[:0]
	e.refresh()
}

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

// decodeKey 从字节源解码一个按键事件（处理控制字符、ESC 转义序列与多字节 UTF-8）。
func decodeKey(ctx context.Context, src byteSource) (keyEvent, error) {
	b, err := src.readByte(ctx)
	if err != nil {
		return keyEvent{}, err
	}
	switch {
	case b == 0x1b:
		return decodeEscape(ctx, src)
	case b == '\r' || b == '\n':
		return ev(keyEnter), nil
	case b == '\t':
		return ev(keyTab), nil
	case b == 0x7f || b == 0x08:
		return ev(keyBackspace), nil
	case b == 0x03:
		return ev(keyCtrlC), nil
	case b == 0x04:
		return ev(keyCtrlD), nil
	case b == 0x01:
		return ev(keyHome), nil
	case b == 0x05:
		return ev(keyEnd), nil
	case b == 0x07:
		return ev(keyCtrlG), nil
	case b == 0x10: // Ctrl-P
		return ev(keyUp), nil
	case b == 0x0e: // Ctrl-N
		return ev(keyDown), nil
	case b == 0x12:
		return ev(keyCtrlR), nil
	case b == 0x15:
		return ev(keyCtrlU), nil
	case b < 0x20:
		return ev(keyUnknown), nil
	case b < 0x80:
		return runeEvent(rune(b)), nil
	default:
		return decodeUTF8(ctx, src, b)
	}
}

// decodeEscape 解码 ESC 引导的转义序列（方向键/Home/End 等）；孤立 ESC 返回 keyEsc。
func decodeEscape(ctx context.Context, src byteSource) (keyEvent, error) {
	lead, ok := src.readByteNow()
	if !ok {
		return ev(keyEsc), nil
	}
	if lead != '[' && lead != 'O' {
		src.unread(lead)
		return ev(keyEsc), nil
	}
	c, err := src.readByte(ctx)
	if err != nil {
		return ev(keyEsc), nil
	}
	if c >= '0' && c <= '9' {
		return decodeNumericEscape(ctx, src, c), nil
	}
	return ev(escapeFinalToKey(c)), nil
}

// escapeFinalToKey 把 ESC[<letter> 的末字母映射为按键。
func escapeFinalToKey(c byte) keyType {
	switch c {
	case 'A':
		return keyUp
	case 'B':
		return keyDown
	case 'C':
		return keyRight
	case 'D':
		return keyLeft
	case 'H':
		return keyHome
	case 'F':
		return keyEnd
	default:
		return keyUnknown
	}
}

// decodeNumericEscape 解析形如 ESC[<digits>~ 的序列（Home/End/Delete 等），读到 '~' 结束。
func decodeNumericEscape(ctx context.Context, src byteSource, first byte) keyEvent {
	seq := []byte{first}
	for {
		n, err := src.readByte(ctx)
		if err != nil || n == '~' {
			break
		}
		seq = append(seq, n)
	}
	switch string(seq) {
	case "1", "7":
		return ev(keyHome)
	case "4", "8":
		return ev(keyEnd)
	default:
		return ev(keyUnknown)
	}
}

// decodeUTF8 读取多字节 UTF-8 rune 的后续字节并解码为字符事件。
func decodeUTF8(ctx context.Context, src byteSource, first byte) (keyEvent, error) {
	n := utf8ByteLen(first)
	buf := make([]byte, 1, 4)
	buf[0] = first
	for i := 1; i < n; i++ {
		b, err := src.readByte(ctx)
		if err != nil {
			return ev(keyUnknown), nil
		}
		buf = append(buf, b)
	}
	r, _ := utf8.DecodeRune(buf)
	if r == utf8.RuneError {
		return ev(keyUnknown), nil
	}
	return runeEvent(r), nil
}

// utf8ByteLen 由 UTF-8 首字节推断该 rune 的字节长度。
func utf8ByteLen(b byte) int {
	switch {
	case b&0xE0 == 0xC0:
		return 2
	case b&0xF0 == 0xE0:
		return 3
	case b&0xF8 == 0xF0:
		return 4
	default:
		return 1
	}
}

// byteSource 抽象逐字节输入：readByte 阻塞直到字节或 ctx 取消，readByteNow 单次轮询，unread 回退一字节。
type byteSource interface {
	readByte(ctx context.Context) (byte, error)
	readByteNow() (byte, bool)
	unread(b byte)
}

// rawInput 是基于 *os.File 的 byteSource：内部缓冲 + 一字节回退，靠终端 VTIME 轮询实现可取消阻塞读。
type rawInput struct {
	f     *os.File
	buf   [256]byte
	n, i  int
	ungot int // <0 表示无回退字节
}

// newRawInput 构造读终端 f 的字节源。
func newRawInput(f *os.File) *rawInput { return &rawInput{f: f, ungot: -1} }

// unread 回退一个字节，供 ESC 序列判定失败时归还。
func (r *rawInput) unread(b byte) { r.ungot = int(b) }

// fill 从终端读取一批字节；返回 got=false 表示 VTIME 超时（无数据，正常轮询）。
// 注意：VMIN=0/VTIME>0 下，无数据时底层 read(2) 返回 0 字节，Go 的 os.File.Read
// 会将 n==0 转为 io.EOF——这不是真正的文件结束，只是 VTIME 超时，需特判忽略。
func (r *rawInput) fill() (got bool, err error) {
	m, err := r.f.Read(r.buf[:])
	if m > 0 {
		r.n, r.i = m, 0
		return true, nil
	}
	// m==0: VTIME 超时或真 EOF。对终端而言 VMIN=0/VTIME>0 返回 0 是正常的"暂无数据"，
	// Go 附带的 io.EOF 应忽略；只有非 EOF 的真实错误（如 EIO/设备断开）才向上冒泡。
	if err == io.EOF || err == nil {
		return false, nil
	}
	return false, err
}

// readByte 阻塞取下一字节；ctx 取消或读到 EOF 时返回错误。
func (r *rawInput) readByte(ctx context.Context) (byte, error) {
	if r.ungot >= 0 {
		b := byte(r.ungot)
		r.ungot = -1
		return b, nil
	}
	for {
		if r.i < r.n {
			b := r.buf[r.i]
			r.i++
			return b, nil
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if _, err := r.fill(); err != nil {
			return 0, err
		}
	}
}

// readByteNow 单次轮询取字节：无缓冲且本次轮询无数据时返回 ok=false（用于孤立 ESC 判定）。
func (r *rawInput) readByteNow() (byte, bool) {
	if r.ungot >= 0 {
		b := byte(r.ungot)
		r.ungot = -1
		return b, true
	}
	if r.i < r.n {
		b := r.buf[r.i]
		r.i++
		return b, true
	}
	if got, err := r.fill(); err != nil || !got {
		return 0, false
	}
	b := r.buf[r.i]
	r.i++
	return b, true
}

// 确保 rawInput 满足 byteSource（编译期校验）。
var _ byteSource = (*rawInput)(nil)

// errNotTerminal 在非终端 fd 上尝试进入 raw 模式时返回。
var errNotTerminal = errors.New("not a terminal")

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
