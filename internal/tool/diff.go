// Package tool 中的 diff.go 生成行级 unified diff，供 edit_file/write_file 把改动
// 以标准 diff 文本填入 ToolResult.Diff，交由终端渲染层着色。纯函数、无副作用。
package tool

import (
	"fmt"
	"strings"
)

// diffContextLines 是 unified diff 每个 hunk 前后保留的上下文行数。
const diffContextLines = 3

// maxDiffInputLines 是参与 diff 的单侧最大行数：超过则不产 diff（渲染层退回摘要），
// 避免超大文件的 O(n*m) LCS 拖慢工具返回。
const maxDiffInputLines = 1200

// opKind 是一次行级编辑操作的类别。
type opKind int

const (
	opEqual opKind = iota // 两侧相同的上下文行
	opDel                 // 仅旧文件存在（删除）
	opIns                 // 仅新文件存在（新增）
)

// diffOp 是一条带行文本的编辑操作。
type diffOp struct {
	kind opKind
	text string
}

// unifiedDiff 生成 oldText→newText 的 unified diff（带 path 头）；两者相同或输入过大时返回空串。
func unifiedDiff(path, oldText, newText string) string {
	if oldText == newText {
		return ""
	}
	a := strings.Split(oldText, "\n")
	b := strings.Split(newText, "\n")
	if len(a) > maxDiffInputLines || len(b) > maxDiffInputLines {
		return ""
	}
	ops := diffOps(a, b)
	body := formatHunks(ops)
	if body == "" {
		return ""
	}
	header := fmt.Sprintf("--- a/%s\n+++ b/%s\n", path, path)
	return header + body
}

// diffOps 用 LCS 动态规划回溯出从 a 到 b 的逐行编辑序列。
func diffOps(a, b []string) []diffOp {
	la, lb := len(a), len(b)
	dp := make([][]int, la+1)
	for i := range dp {
		dp[i] = make([]int, lb+1)
	}
	for i := la - 1; i >= 0; i-- {
		for j := lb - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	ops := make([]diffOp, 0, la+lb)
	i, j := 0, 0
	for i < la && j < lb {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{opEqual, a[i]})
			i, j = i+1, j+1
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, diffOp{opDel, a[i]})
			i++
		default:
			ops = append(ops, diffOp{opIns, b[j]})
			j++
		}
	}
	for ; i < la; i++ {
		ops = append(ops, diffOp{opDel, a[i]})
	}
	for ; j < lb; j++ {
		ops = append(ops, diffOp{opIns, b[j]})
	}
	return ops
}

// interval 是一段 ops 下标闭区间 [start,end]，代表一个待输出的 hunk。
type interval struct{ start, end int }

// formatHunks 把编辑序列按上下文聚合为若干 hunk 并格式化为 unified diff 主体。
func formatHunks(ops []diffOp) string {
	oldAt, newAt := lineNumbers(ops)
	groups := hunkIntervals(ops)
	if len(groups) == 0 {
		return ""
	}
	var b strings.Builder
	for _, g := range groups {
		writeHunk(&b, ops, oldAt, newAt, g)
	}
	return b.String()
}

// lineNumbers 预计算每个 op 进入时的 1-based 旧/新行号。
func lineNumbers(ops []diffOp) (oldAt, newAt []int) {
	oldAt = make([]int, len(ops))
	newAt = make([]int, len(ops))
	oldLine, newLine := 1, 1
	for i, op := range ops {
		oldAt[i], newAt[i] = oldLine, newLine
		switch op.kind {
		case opEqual:
			oldLine, newLine = oldLine+1, newLine+1
		case opDel:
			oldLine++
		case opIns:
			newLine++
		default:
			// 不应发生：opKind 仅三值。
		}
	}
	return oldAt, newAt
}

// hunkIntervals 以每个变更点为中心扩展 diffContextLines 行，合并重叠/相邻区间。
func hunkIntervals(ops []diffOp) []interval {
	var out []interval
	for i, op := range ops {
		if op.kind == opEqual {
			continue
		}
		s := i - diffContextLines
		if s < 0 {
			s = 0
		}
		e := i + diffContextLines
		if e > len(ops)-1 {
			e = len(ops) - 1
		}
		if n := len(out); n > 0 && s <= out[n-1].end+1 {
			out[n-1].end = e
			continue
		}
		out = append(out, interval{s, e})
	}
	return out
}

// writeHunk 输出单个 hunk：先写 @@ 头，再按前缀写各行。
func writeHunk(b *strings.Builder, ops []diffOp, oldAt, newAt []int, g interval) {
	oldStart, newStart := oldAt[g.start], newAt[g.start]
	oldCount, newCount := 0, 0
	for i := g.start; i <= g.end; i++ {
		switch ops[i].kind {
		case opEqual:
			oldCount, newCount = oldCount+1, newCount+1
		case opDel:
			oldCount++
		case opIns:
			newCount++
		default:
			// 不应发生。
		}
	}
	fmt.Fprintf(b, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
	for i := g.start; i <= g.end; i++ {
		b.WriteString(linePrefix(ops[i].kind))
		b.WriteString(ops[i].text)
		b.WriteByte('\n')
	}
}

// linePrefix 返回 unified diff 行前缀：上下文空格、删除 '-'、新增 '+'。
func linePrefix(k opKind) string {
	switch k {
	case opDel:
		return "-"
	case opIns:
		return "+"
	default:
		return " "
	}
}
