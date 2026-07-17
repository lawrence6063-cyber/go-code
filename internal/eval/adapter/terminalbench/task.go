// 本文件定义 Terminal-Bench 任务 task.yaml 的机器映射与极简手写解析器（EVAL_SPEC §5.2 / §8.5
// 「先手写极简解析器守零依赖」）。相较 native 的平坦 task.yaml，Terminal-Bench 的 instruction 常为
// 多行块标量（`instruction: |`），故解析器额外支持块标量与块列表（tags）。
package terminalbench

import (
	"strings"
)

// TaskYAML 是 Terminal-Bench 任务 task.yaml 中评测关心的子集。
type TaskYAML struct {
	Instruction string   // 任务指令（喂给 agent 的自然语言意图；常为多行块标量）
	Difficulty  string   // easy | medium | hard（Terminal-Bench 多为 hard）
	Tags        []string // 领域标签（file-manipulation | security | data-science ...）
}

// Filter 按 task id / tag / 难度筛选，并可限制取样数量（个人项目取子集，EVAL_SPEC §5.2.3）。
type Filter struct {
	IDs          []string // 只跑这些 task id（目录名，空=不限）
	Tags         []string // 只跑含这些标签之一的任务（空=不限）
	Difficulties []string // 只跑这些难度（空=不限）
	Limit        int      // 最多取 N 个（<=0=不限）
}

// parseTaskYAML 解析 task.yaml：支持顶层标量、内联数组、块列表（tags）与块标量（instruction）。
// 容错优先：无法识别的键忽略，不返回错误（缺字段由上层兜底）。
func parseTaskYAML(data []byte) TaskYAML {
	var y TaskYAML
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if line == "" || line[0] == ' ' || line[0] == '\t' || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue // 只处理顶层键；嵌套/块内容由块收集器消费
		}
		key, val := splitKV(line)
		if key == "" {
			continue
		}
		switch key {
		case "instruction", "description":
			if isBlockScalar(val) {
				block, next := collectBlock(lines, i+1)
				y.Instruction, i = block, next-1
			} else if v := unquote(val); v != "" {
				y.Instruction = v
			}
		case "difficulty":
			y.Difficulty = unquote(val)
		case "tags":
			if list := parseInlineList(val); list != nil {
				y.Tags = list
			} else if strings.TrimSpace(val) == "" {
				items, next := collectBlockList(lines, i+1)
				y.Tags, i = items, next-1
			}
		}
	}
	return y
}

// isBlockScalar 报告值是否为 YAML 块标量指示符（| 或 >，含 |- / |+ / >- 变体）。
func isBlockScalar(val string) bool {
	v := strings.TrimSpace(val)
	return strings.HasPrefix(v, "|") || strings.HasPrefix(v, ">")
}

// collectBlock 从 start 收集块标量的缩进内容行，去除公共缩进后拼回，返回(文本, 下一个未消费行号)。
func collectBlock(lines []string, start int) (string, int) {
	var body []string
	i := start
	for ; i < len(lines); i++ {
		l := lines[i]
		if strings.TrimSpace(l) == "" {
			body = append(body, "")
			continue
		}
		if l[0] != ' ' && l[0] != '\t' {
			break // 回到顶层，块结束
		}
		body = append(body, l)
	}
	return strings.TrimSpace(dedent(body)), i
}

// collectBlockList 收集形如 "  - item" 的块列表项，返回(项列表, 下一个未消费行号)。
func collectBlockList(lines []string, start int) ([]string, int) {
	var items []string
	i := start
	for ; i < len(lines); i++ {
		l := lines[i]
		if strings.TrimSpace(l) == "" {
			continue
		}
		if l[0] != ' ' && l[0] != '\t' {
			break
		}
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "-") {
			break
		}
		if v := unquote(strings.TrimSpace(strings.TrimPrefix(t, "-"))); v != "" {
			items = append(items, v)
		}
	}
	return items, i
}

// dedent 去除多行文本的公共前导空白（以首个非空行的缩进为准）。
func dedent(lines []string) string {
	prefix := ""
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		prefix = l[:len(l)-len(strings.TrimLeft(l, " \t"))]
		break
	}
	var b strings.Builder
	for i, l := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(strings.TrimPrefix(l, prefix))
	}
	return b.String()
}

// splitKV 按首个冒号切出 key/value；无冒号返回空 key。
func splitKV(line string) (string, string) {
	i := strings.Index(line, ":")
	if i < 0 {
		return "", ""
	}
	return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:])
}

// unquote 去除标量两端的成对引号与首尾空格。
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// parseInlineList 解析内联数组 "[a, b]"；非数组返回 nil（区分「空块列表」与「非列表」）。
func parseInlineList(s string) []string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return []string{}
	}
	parts := strings.Split(inner, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := unquote(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}
