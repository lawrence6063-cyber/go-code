// Package completion 提供终端 @ 文件补全的纯逻辑与候选来源：
// token.go 负责从输入行中解析 @ 触发片段并把选中项写回，
// provider.go 负责从工作区收集候选文件并做内存模糊过滤。
package completion

import "unicode"

// atTrigger 是文件补全的触发符。
const atTrigger = '@'

// AtToken 描述输入行中一次 @ 补全上下文的解析结果。
type AtToken struct {
	Start   int    // @ 符号在行中的 rune 下标
	Partial string // @ 之后到光标之间的已输入片段
	Active  bool   // 是否处于有效的 @ 补全上下文
}

// ParseAtToken 解析光标前最近的 @ 触发 token：从光标向左回溯到最近的 @，
// 期间若遇到空白字符则视为不在补全上下文（Active=false）。cursor 为 rune 下标，
// 取值范围 [0, len(line)]，越界时被夹取到合法区间。
func ParseAtToken(line []rune, cursor int) AtToken {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(line) {
		cursor = len(line)
	}
	for i := cursor - 1; i >= 0; i-- {
		r := line[i]
		if r == atTrigger {
			return AtToken{Start: i, Partial: string(line[i+1 : cursor]), Active: true}
		}
		if unicode.IsSpace(r) {
			return AtToken{Active: false}
		}
	}
	return AtToken{Active: false}
}

// ApplyChoice 把 @partial 片段替换为 @choice（choice 为工作区相对路径，不含 @），
// 保留 @ 前缀，返回新行与新光标位置（定位到写回路径末尾）。start 指向 @ 的下标，
// cursor 指向片段末尾（即原 partial 的结束位置）。
func ApplyChoice(line []rune, cursor, start int, choice string) (newLine []rune, newCursor int) {
	if start < 0 || start >= len(line) || line[start] != atTrigger {
		return line, cursor // 非法上下文：原样返回，避免破坏输入
	}
	if cursor < start+1 {
		cursor = start + 1
	}
	if cursor > len(line) {
		cursor = len(line)
	}
	replacement := append([]rune{atTrigger}, []rune(choice)...)
	out := make([]rune, 0, start+len(replacement)+(len(line)-cursor))
	out = append(out, line[:start]...)
	out = append(out, replacement...)
	newCursor = len(out)
	out = append(out, line[cursor:]...)
	return out, newCursor
}
