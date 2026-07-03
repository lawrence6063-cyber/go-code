package tui

import (
	"context"
	"time"

	"github.com/alaindong/cogent/internal/tui/completion"
	"github.com/alaindong/cogent/internal/tui/history"
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
