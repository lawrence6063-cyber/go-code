package render

import "strings"

// maxDiffLines 是 diff 着色的行数上限：超过则降级为纯文本原样返回，避免超大 diff
// 拖慢热路径渲染。
const maxDiffLines = 2000

// ColorizeDiff 把 unified diff 文本按语义着色：新增行（+）绿、删除行（-）红、
// hunk 头（@@）青、文件头（diff/index/+++/---）加粗、上下文行灰。
// 空输入返回空串；行数超过 maxDiffLines 时降级为纯文本原样返回。
func ColorizeDiff(unified string) string {
	if unified == "" {
		return ""
	}
	lines := strings.Split(unified, "\n")
	if len(lines) > maxDiffLines {
		return unified
	}
	var b strings.Builder
	b.Grow(len(unified) + len(unified)/4)
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(colorizeDiffLine(line))
	}
	return b.String()
}

// colorizeDiffLine 为单行 diff 选择着色：文件头/hunk 头优先于增删判定，
// 避免把 "+++"/"---" 误当作新增/删除行。
func colorizeDiffLine(line string) string {
	switch {
	case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"),
		strings.HasPrefix(line, "diff "), strings.HasPrefix(line, "index "):
		return Bold(line)
	case strings.HasPrefix(line, "@@"):
		return Cyan(line)
	case strings.HasPrefix(line, "+"):
		return Green(line)
	case strings.HasPrefix(line, "-"):
		return Red(line)
	default:
		return Gray(line)
	}
}
