// 本文件实现 diff 归一化（去重用）与改动规模统计（破平用），均为纯字符串处理（SCAFFOLD_SPEC §4.1）。
package scaffold

import (
	"sort"
	"strings"
)

// NormalizeDiff 归一化补丁用于去重：剥离 diff 头噪声（index 行、blob 哈希、@@ 行号），
// 统一行尾空白，并按文件块排序（文件出现顺序无关），得到「行序无关、不改语义」的可比形式。
// 块内行序保留（是语义），仅规避 header 噪声与文件排列差异造成的假性不同。
func NormalizeDiff(patch string) string {
	blocks := splitFileBlocks(patch)
	norm := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if s := normalizeBlock(b); s != "" {
			norm = append(norm, s)
		}
	}
	sort.Strings(norm)
	return strings.Join(norm, "\n")
}

// splitFileBlocks 按 "diff --git" 边界切分补丁为若干文件块；无该标记时整体作为单块。
func splitFileBlocks(patch string) []string {
	lines := strings.Split(patch, "\n")
	var blocks []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, ln := range lines {
		if strings.HasPrefix(ln, "diff --git ") {
			flush()
		}
		cur = append(cur, ln)
	}
	flush()
	return blocks
}

// normalizeBlock 归一化单个文件块：丢弃 index / blob 噪声行，把 @@ 头的行号抹平为 "@@"，
// 去行尾空白，去首尾空行。
func normalizeBlock(block string) string {
	lines := strings.Split(block, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		ln = strings.TrimRight(ln, " \t\r")
		switch {
		case strings.HasPrefix(ln, "index "):
			continue // blob 哈希/mode，去重无意义
		case strings.HasPrefix(ln, "@@"):
			out = append(out, "@@") // 抹平行号区间，只保留 hunk 分隔语义
		default:
			out = append(out, ln)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// diffStats 统计补丁触及的文件数与改动（+/-）行数，用于最小改动破平。
// 文件数按 "diff --git" / "+++ " 计；改动行按以单个 '+' 或 '-' 起始且非文件头（+++/---）计。
func diffStats(patch string) (files, lines int) {
	seen := make(map[string]struct{})
	for _, ln := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(ln, "diff --git "):
			if p := fileFromDiffGit(ln); p != "" {
				seen[p] = struct{}{}
			}
		case strings.HasPrefix(ln, "+++ ") || strings.HasPrefix(ln, "--- "):
			if p := strings.TrimSpace(ln[4:]); p != "" && p != "/dev/null" {
				seen[strings.TrimPrefix(strings.TrimPrefix(p, "a/"), "b/")] = struct{}{}
			}
		case strings.HasPrefix(ln, "+") || strings.HasPrefix(ln, "-"):
			lines++
		}
	}
	return len(seen), lines
}

// fileFromDiffGit 从 "diff --git a/x b/x" 行提取目标文件路径（去 b/ 前缀）；解析失败返回空。
func fileFromDiffGit(ln string) string {
	fields := strings.Fields(ln)
	if len(fields) < 4 {
		return ""
	}
	return strings.TrimPrefix(fields[3], "b/")
}
