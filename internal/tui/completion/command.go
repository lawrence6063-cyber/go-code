package completion

import "strings"

// slashTrigger 是斜杠命令补全的触发符。
const slashTrigger = '/'

// Command 描述一条内建斜杠命令的补全条目。
type Command struct {
	Name string // 命令名（含前导 /，如 "/undo"）
	Desc string // 一句话说明，供 /help 与下拉展示
}

// SlashToken 描述输入行中一次斜杠命令补全上下文的解析结果。
type SlashToken struct {
	Partial string // 行首到光标之间的命令片段（含前导 /）
	Active  bool   // 是否处于有效的斜杠命令补全上下文
}

// CommandProvider 提供内建斜杠命令候选：按已输入片段做前缀过滤。
type CommandProvider interface {
	// Filter 返回与 partial 前缀匹配的命令（保持注册顺序）；limit<=0 表示不限制条数。
	Filter(partial string, limit int) []Command
}

// builtinCommands 是内建斜杠命令注册表，作为补全与 /help 的单一事实来源。
var builtinCommands = []Command{
	{Name: "/help", Desc: "显示可用命令"},
	{Name: "/undo", Desc: "撤销上一轮改动"},
	{Name: "/model", Desc: "显示当前模型"},
	{Name: "/clear", Desc: "清空当前会话上下文"},
	{Name: "/compact", Desc: "压缩当前会话上下文"},
	{Name: "/exit", Desc: "退出交互"},
}

// commandProvider 是基于内建注册表的 CommandProvider 实现。
type commandProvider struct {
	cmds []Command
}

// NewCommandProvider 构造基于内建命令注册表的补全来源。
func NewCommandProvider() CommandProvider {
	return &commandProvider{cmds: builtinCommands}
}

// Filter 见 CommandProvider 接口说明：按 partial 做命令名前缀匹配。
func (p *commandProvider) Filter(partial string, limit int) []Command {
	out := make([]Command, 0, len(p.cmds))
	for _, c := range p.cmds {
		if !strings.HasPrefix(c.Name, partial) {
			continue
		}
		out = append(out, c)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// ParseSlashToken 解析行首的斜杠命令 token：仅当行以 / 开头、且光标落在命令名
// （首个空格之前）范围内时视为激活。cursor 为 rune 下标，越界时夹取到合法区间。
func ParseSlashToken(line []rune, cursor int) SlashToken {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(line) {
		cursor = len(line)
	}
	if len(line) == 0 || line[0] != slashTrigger {
		return SlashToken{Active: false}
	}
	if sp := firstSpace(line); sp >= 0 && cursor > sp {
		return SlashToken{Active: false} // 光标已进入参数区，不再补全命令名
	}
	return SlashToken{Partial: string(line[:cursor]), Active: true}
}

// ApplySlashChoice 用选中的命令名替换行首命令片段，保留其后的参数与空格，
// 返回新行与定位到命令名末尾的新光标。
func ApplySlashChoice(line []rune, choice string) (newLine []rune, newCursor int) {
	repl := []rune(choice)
	sp := firstSpace(line)
	if sp < 0 {
		return repl, len(repl)
	}
	out := make([]rune, 0, len(repl)+len(line)-sp)
	out = append(out, repl...)
	out = append(out, line[sp:]...)
	return out, len(repl)
}

// firstSpace 返回 line 中第一个空格的 rune 下标；不含空格时返回 -1。
func firstSpace(line []rune) int {
	for i, r := range line {
		if r == ' ' {
			return i
		}
	}
	return -1
}
