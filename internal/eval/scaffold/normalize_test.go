package scaffold

import "testing"

// TestNormalizeDiff_HeaderNoiseEquivalent 覆盖「去重正确」：仅 index/@@ 行号/行尾空白不同的补丁归一化后相等。
func TestNormalizeDiff_HeaderNoiseEquivalent(t *testing.T) {
	if NormalizeDiff(diffA) != NormalizeDiff(diffAvariant) {
		t.Errorf("header-noise variants should normalize equal:\nA=%q\nvariant=%q",
			NormalizeDiff(diffA), NormalizeDiff(diffAvariant))
	}
}

// TestNormalizeDiff_DistinctPatchesDiffer 覆盖语义不同的补丁归一化后不相等。
func TestNormalizeDiff_DistinctPatchesDiffer(t *testing.T) {
	if NormalizeDiff(diffA) == NormalizeDiff(diffB) {
		t.Error("semantically different patches must not normalize equal")
	}
}

// TestNormalizeDiff_FileOrderIndependent 覆盖「行序无关」：文件块顺序不同的补丁归一化后相等。
func TestNormalizeDiff_FileOrderIndependent(t *testing.T) {
	ab := diffA + diffB
	ba := diffB + diffA
	if NormalizeDiff(ab) != NormalizeDiff(ba) {
		t.Error("file-block order should not affect normalization")
	}
}

// TestNormalizeDiff_Empty 覆盖空补丁归一化为空串。
func TestNormalizeDiff_Empty(t *testing.T) {
	if got := NormalizeDiff("   \n\t\n"); got != "" {
		t.Errorf("empty patch should normalize to empty, got %q", got)
	}
}

// TestDiffStats 覆盖文件数与改动行数统计。
func TestDiffStats(t *testing.T) {
	files, lines := diffStats(diffA)
	if files != 1 {
		t.Errorf("diffA files = %d, want 1", files)
	}
	if lines != 2 { // 一减一加（+++/--- 文件头不计）
		t.Errorf("diffA lines = %d, want 2", lines)
	}
	twoFiles := diffA + diffB
	if f, _ := diffStats(twoFiles); f != 2 {
		t.Errorf("two-file diff files = %d, want 2", f)
	}
}
