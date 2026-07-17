// Package scaffold 实现 SWE-bench test-time scaling 的选择器（SCAFFOLD_SPEC §4.1，S-D 阶段）。
//
// 它是评测层的纯逻辑叶子：只依赖标准库，输入「候选补丁集合 + 可执行信号」，输出「最终补丁 + 选择理由」。
// 绝不做网络/Docker/文件 I/O（守 SCAFFOLD_SPEC §3.3 不变量），仅被 cmd 层与文件聚合器调用，
// 绝不被内核反向 import（守 EVAL_SPEC §5.3 依赖只能向内）。
//
// 选择策略（对齐 Agentless：先用可执行信号硬过滤，再对存活候选归一化去重后多数投票）：
//  1. 过滤——淘汰 apply 失败的候选；复现/回归信号「存在且为假」才否决（信号缺失不参与否决）。
//  2. 投票——对存活候选按 NormalizeDiff 归一化分组，取票数最多的组。
//  3. 破平——票数并列时按 (复现通过 > 回归不破 > 最小改动:文件数↑行数) 打分，再以序号兜底确定性。
package scaffold

import (
	"fmt"
	"sort"
)

// Candidate 是一个候选补丁及其可执行信号结果（SCAFFOLD_SPEC §4.1）。
// HaveRepro / HaveRegress 标记信号是否可用：为 false 时对应的 ReproPassed / RegressionOK 不参与否决。
type Candidate struct {
	Index        int    // 采样序号（best-of-N 的第 k 次）
	Patch        string // 统一 diff（git a/.. b/..）
	Applied      bool   // 是否能干净 apply（不能 apply 直接淘汰）
	ReproPassed  bool   // 复现测试是否通过
	RegressionOK bool   // 回归测试是否未被打破
	HaveRepro    bool   // 是否有复现测试信号
	HaveRegress  bool   // 是否有回归信号
}

// eligible 报告候选是否通过硬过滤：必须能 apply；复现/回归信号存在时必须为真（缺失不否决）。
func (c Candidate) eligible() bool {
	if !c.Applied {
		return false
	}
	if c.HaveRepro && !c.ReproPassed {
		return false
	}
	if c.HaveRegress && !c.RegressionOK {
		return false
	}
	return true
}

// group 是一组归一化后等价的候选补丁及其聚合投票信号。
type group struct {
	norm       string      // 归一化 diff（分组键）
	patch      string      // 代表补丁（组内序号最小者的原始补丁）
	members    []Candidate // 组内候选
	repro      bool        // 组内是否有「复现通过」证据
	regress    bool        // 组内是否有「回归不破」证据
	files      int         // 代表补丁触及文件数（越小越优）
	lines      int         // 代表补丁改动行数（越小越优）
	minIndex   int         // 组内最小序号（破平兜底，保证确定性）
	voteWeight int         // 票数（组内成员数）
}

// Select 从候选集选出最终补丁（SCAFFOLD_SPEC §4.1）。
// 返回选中补丁与人类可读的选择理由；无任何可提交候选时返回 ("", reason)。
//
// 分层：先按 eligible() 硬过滤；若无存活候选，则退回「所有能 apply 的候选」再投票（避免因测试信号
// 全否决而提交空补丁——空补丁必然判负）；两者皆空（无任何可 apply 候选）才返回空。
func Select(cands []Candidate) (patch string, reason string) {
	if len(cands) == 0 {
		return "", "no candidates"
	}
	survivors := filter(cands, Candidate.eligible)
	fallback := false
	if len(survivors) == 0 {
		survivors = filter(cands, func(c Candidate) bool { return c.Applied })
		fallback = true
	}
	if len(survivors) == 0 {
		return "", fmt.Sprintf("no applicable candidate: all %d candidate(s) failed to apply", len(cands))
	}
	best := pickBestGroup(groupBy(survivors))
	return best.patch, explain(best, len(cands), len(survivors), fallback)
}

// filter 返回 cands 中满足 keep 的子集（保持原序）。
func filter(cands []Candidate, keep func(Candidate) bool) []Candidate {
	out := make([]Candidate, 0, len(cands))
	for _, c := range cands {
		if keep(c) {
			out = append(out, c)
		}
	}
	return out
}

// groupBy 把候选按 NormalizeDiff 归一化键分组，并聚合投票信号（票数/证据/最小改动/最小序号）。
// 返回按确定性顺序（键升序）排列的组切片。
func groupBy(cands []Candidate) []group {
	byNorm := make(map[string]*group)
	var order []string
	for _, c := range cands {
		key := NormalizeDiff(c.Patch)
		g, ok := byNorm[key]
		if !ok {
			files, lines := diffStats(c.Patch)
			g = &group{norm: key, patch: c.Patch, files: files, lines: lines, minIndex: c.Index}
			byNorm[key] = g
			order = append(order, key)
		}
		g.members = append(g.members, c)
		g.voteWeight++
		if c.HaveRepro && c.ReproPassed {
			g.repro = true
		}
		if c.HaveRegress && c.RegressionOK {
			g.regress = true
		}
		if c.Index < g.minIndex {
			g.minIndex = c.Index
			g.patch = c.Patch // 代表补丁取组内序号最小者，保证确定性
		}
	}
	sort.Strings(order)
	groups := make([]group, 0, len(order))
	for _, k := range order {
		groups = append(groups, *byNorm[k])
	}
	return groups
}

// pickBestGroup 选出胜出组：先比票数，平票依次比 (复现证据 > 回归证据 > 文件更少 > 行更少 > 序号更小)。
func pickBestGroup(groups []group) group {
	best := groups[0]
	for _, g := range groups[1:] {
		if better(g, best) {
			best = g
		}
	}
	return best
}

// better 报告候选组 a 是否优于当前最优 b（多数投票 + 分层破平）。
func better(a, b group) bool {
	if a.voteWeight != b.voteWeight {
		return a.voteWeight > b.voteWeight
	}
	if a.repro != b.repro {
		return a.repro
	}
	if a.regress != b.regress {
		return a.regress
	}
	if a.files != b.files {
		return a.files < b.files
	}
	if a.lines != b.lines {
		return a.lines < b.lines
	}
	return a.minIndex < b.minIndex
}

// explain 生成选择理由，供报告解释为何选中该补丁。
func explain(g group, total, survivors int, fallback bool) string {
	base := fmt.Sprintf("selected group with %d/%d vote(s) among %d survivor(s) of %d candidate(s)",
		g.voteWeight, survivors, survivors, total)
	if fallback {
		base += " [fallback: no candidate passed test filter, voted among applied-only]"
	}
	base += fmt.Sprintf("; signals repro=%v regress=%v; change files=%d lines=%d",
		g.repro, g.regress, g.files, g.lines)
	return base
}
