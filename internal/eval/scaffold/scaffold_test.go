package scaffold

import (
	"strings"
	"testing"
)

// diffA / diffB 是两个语义不同的补丁；diffAvariant 与 diffA 仅 header 噪声/空白不同（应归一化为等价）。
const (
	diffA = `diff --git a/foo.py b/foo.py
index 1111111..2222222 100644
--- a/foo.py
+++ b/foo.py
@@ -10,3 +10,3 @@ def f():
-    return x
+    return x + 1
`
	diffAvariant = `diff --git a/foo.py b/foo.py
index abcdef0..fedcba9 100644
--- a/foo.py
+++ b/foo.py
@@ -20,7 +20,7 @@ class C:
-    return x   
+    return x + 1	
`
	diffB = `diff --git a/bar.py b/bar.py
index 3333333..4444444 100644
--- a/bar.py
+++ b/bar.py
@@ -5,2 +5,3 @@ def g():
     y = 1
+    y += 2
`
)

// TestSelect_AllApplyFail 覆盖「全 apply 失败」→ 返回空补丁。
func TestSelect_AllApplyFail(t *testing.T) {
	cands := []Candidate{
		{Index: 0, Patch: diffA, Applied: false},
		{Index: 1, Patch: diffB, Applied: false},
	}
	patch, reason := Select(cands)
	if patch != "" {
		t.Errorf("patch = %q, want empty", patch)
	}
	if !strings.Contains(reason, "failed to apply") {
		t.Errorf("reason = %q, want mention of apply failure", reason)
	}
}

// TestSelect_EmptyInput 覆盖空候选集。
func TestSelect_EmptyInput(t *testing.T) {
	if patch, _ := Select(nil); patch != "" {
		t.Errorf("patch = %q, want empty for nil input", patch)
	}
}

// TestSelect_NoSignalsPureVote 覆盖「无任何测试信号 → 退化为纯多数投票」。
// diffA 出现 3 次（含 1 个 header 变体），diffB 出现 2 次 → diffA 组胜出。
func TestSelect_NoSignalsPureVote(t *testing.T) {
	cands := []Candidate{
		{Index: 0, Patch: diffA, Applied: true},
		{Index: 1, Patch: diffB, Applied: true},
		{Index: 2, Patch: diffAvariant, Applied: true},
		{Index: 3, Patch: diffB, Applied: true},
		{Index: 4, Patch: diffA, Applied: true},
	}
	patch, reason := Select(cands)
	if NormalizeDiff(patch) != NormalizeDiff(diffA) {
		t.Errorf("selected patch not in diffA group; reason=%s", reason)
	}
	if !strings.Contains(reason, "3/5") {
		t.Errorf("reason = %q, want 3/5 vote", reason)
	}
}

// TestSelect_ReproDecidesWinner 覆盖「复现信号决定胜者」：多数派 diffB 复现失败被否决，
// 少数派 diffA 复现通过 → diffA 胜出（硬过滤优先于投票）。
func TestSelect_ReproDecidesWinner(t *testing.T) {
	cands := []Candidate{
		{Index: 0, Patch: diffB, Applied: true, HaveRepro: true, ReproPassed: false},
		{Index: 1, Patch: diffB, Applied: true, HaveRepro: true, ReproPassed: false},
		{Index: 2, Patch: diffB, Applied: true, HaveRepro: true, ReproPassed: false},
		{Index: 3, Patch: diffA, Applied: true, HaveRepro: true, ReproPassed: true},
	}
	patch, reason := Select(cands)
	if NormalizeDiff(patch) != NormalizeDiff(diffA) {
		t.Errorf("expected repro-passing diffA to win, got other; reason=%s", reason)
	}
	if strings.Contains(reason, "fallback") {
		t.Errorf("should not be fallback when a candidate passes repro; reason=%s", reason)
	}
}

// TestSelect_RegressionVetoes 覆盖回归信号否决：打破回归的候选被淘汰。
func TestSelect_RegressionVetoes(t *testing.T) {
	cands := []Candidate{
		{Index: 0, Patch: diffB, Applied: true, HaveRegress: true, RegressionOK: false},
		{Index: 1, Patch: diffA, Applied: true, HaveRegress: true, RegressionOK: true},
	}
	patch, _ := Select(cands)
	if NormalizeDiff(patch) != NormalizeDiff(diffA) {
		t.Error("regression-breaking candidate should be vetoed")
	}
}

// TestSelect_SignalMissingDoesNotVeto 覆盖「信号缺失不参与否决」：无复现信号的候选不因缺失被淘汰。
func TestSelect_SignalMissingDoesNotVeto(t *testing.T) {
	cands := []Candidate{
		{Index: 0, Patch: diffA, Applied: true}, // 无任何信号
	}
	if patch, _ := Select(cands); NormalizeDiff(patch) != NormalizeDiff(diffA) {
		t.Error("candidate without signals should survive filter")
	}
}

// TestSelect_TieBreak 覆盖平票破平：两组各 1 票，A 有复现证据 → A 胜。
func TestSelect_TieBreak(t *testing.T) {
	cands := []Candidate{
		{Index: 0, Patch: diffA, Applied: true, HaveRepro: true, ReproPassed: true},
		{Index: 1, Patch: diffB, Applied: true},
	}
	patch, _ := Select(cands)
	if NormalizeDiff(patch) != NormalizeDiff(diffA) {
		t.Error("tie should break toward repro-evidenced group")
	}
}

// TestSelect_TieBreakMinimalChange 覆盖平票时「最小改动」破平（无测试证据时比文件/行数）。
func TestSelect_TieBreakMinimalChange(t *testing.T) {
	big := `diff --git a/x.py b/x.py
--- a/x.py
+++ b/x.py
@@ -1,3 +1,4 @@
-a
+a2
+b
+c
diff --git a/y.py b/y.py
--- a/y.py
+++ b/y.py
@@ -1 +1 @@
-d
+d2
`
	small := `diff --git a/z.py b/z.py
--- a/z.py
+++ b/z.py
@@ -1 +1 @@
-e
+e2
`
	cands := []Candidate{
		{Index: 0, Patch: big, Applied: true},
		{Index: 1, Patch: small, Applied: true},
	}
	patch, _ := Select(cands)
	if NormalizeDiff(patch) != NormalizeDiff(small) {
		t.Error("tie should break toward smaller change (fewer files/lines)")
	}
}

// TestSelect_FallbackWhenAllVetoed 覆盖：所有候选复现失败但能 apply → 退回纯投票而非提交空补丁。
func TestSelect_FallbackWhenAllVetoed(t *testing.T) {
	cands := []Candidate{
		{Index: 0, Patch: diffA, Applied: true, HaveRepro: true, ReproPassed: false},
		{Index: 1, Patch: diffA, Applied: true, HaveRepro: true, ReproPassed: false},
		{Index: 2, Patch: diffB, Applied: true, HaveRepro: true, ReproPassed: false},
	}
	patch, reason := Select(cands)
	if patch == "" {
		t.Fatal("should fall back to applied-only vote, not empty")
	}
	if !strings.Contains(reason, "fallback") {
		t.Errorf("reason = %q, want fallback marker", reason)
	}
	if NormalizeDiff(patch) != NormalizeDiff(diffA) {
		t.Error("fallback should pick majority (diffA, 2 votes)")
	}
}

// TestSelect_Deterministic 覆盖确定性：同一输入多次调用结果一致。
func TestSelect_Deterministic(t *testing.T) {
	cands := []Candidate{
		{Index: 0, Patch: diffA, Applied: true},
		{Index: 1, Patch: diffB, Applied: true},
	}
	first, _ := Select(cands)
	for i := 0; i < 20; i++ {
		if got, _ := Select(cands); got != first {
			t.Fatal("Select is not deterministic")
		}
	}
}
