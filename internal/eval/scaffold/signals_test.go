package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadSignals 覆盖信号 JSON 反序列化为 index→signal 映射。
func TestLoadSignals(t *testing.T) {
	js := `{"instance_id":"x","candidates":[
		{"index":0,"applied":true,"have_repro":true,"repro_passed":false,"have_regress":true,"regression_ok":true},
		{"index":2,"applied":false}
	]}`
	m, err := LoadSignals(strings.NewReader(js))
	if err != nil {
		t.Fatalf("LoadSignals: %v", err)
	}
	if len(m) != 2 {
		t.Fatalf("want 2 signals, got %d", len(m))
	}
	if !m[0].HaveRepro || m[0].ReproPassed {
		t.Errorf("index0 signal wrong: %+v", m[0])
	}
	if m[2].Applied {
		t.Errorf("index2 should be applied=false: %+v", m[2])
	}
}

// TestLoadArtifacts_WithSignals 覆盖：候选补丁 + 信号 合并为 Candidate，空补丁被跳过。
func TestLoadArtifacts_WithSignals(t *testing.T) {
	dir := t.TempDir()
	id := "django__django-1"
	writeCandidate(t, dir, id, 0, diffA)
	writeCandidate(t, dir, id, 1, diffB)
	writeCandidate(t, dir, id, 2, "   ") // 空补丁应被跳过
	writeSignals(t, dir, id, `{"instance_id":"django__django-1","candidates":[
		{"index":0,"applied":true,"have_repro":true,"repro_passed":true,"have_regress":false,"regression_ok":false},
		{"index":1,"applied":false}
	]}`)

	insts, err := LoadArtifacts(dir)
	if err != nil {
		t.Fatalf("LoadArtifacts: %v", err)
	}
	if len(insts) != 1 || insts[0].InstanceID != id {
		t.Fatalf("want 1 instance %q, got %+v", id, insts)
	}
	cands := insts[0].Candidates
	if len(cands) != 2 { // 空补丁被跳过
		t.Fatalf("want 2 candidates, got %d", len(cands))
	}
	if !cands[0].Applied || !cands[0].HaveRepro || !cands[0].ReproPassed {
		t.Errorf("cand0 signals not applied: %+v", cands[0])
	}
	if cands[1].Applied {
		t.Errorf("cand1 should be applied=false: %+v", cands[1])
	}
	// 端到端：Select 应选出复现通过且能 apply 的 cand0（diffA）。
	patch, _ := Select(cands)
	if NormalizeDiff(patch) != NormalizeDiff(diffA) {
		t.Error("Select should pick repro-passing applied candidate")
	}
}

// TestLoadArtifacts_NoSignalsPureVote 覆盖无 signals 文件 → 退化为纯投票（Applied=true）。
func TestLoadArtifacts_NoSignals(t *testing.T) {
	dir := t.TempDir()
	id := "psf__requests-1"
	writeCandidate(t, dir, id, 0, diffA)
	writeCandidate(t, dir, id, 1, diffA)
	writeCandidate(t, dir, id, 2, diffB)

	insts, err := LoadArtifacts(dir)
	if err != nil {
		t.Fatalf("LoadArtifacts: %v", err)
	}
	if len(insts) != 1 {
		t.Fatalf("want 1 instance, got %d", len(insts))
	}
	for _, c := range insts[0].Candidates {
		if !c.Applied || c.HaveRepro || c.HaveRegress {
			t.Errorf("no-signal candidate should be applied w/o test signals: %+v", c)
		}
	}
	patch, _ := Select(insts[0].Candidates)
	if NormalizeDiff(patch) != NormalizeDiff(diffA) {
		t.Error("pure vote should pick diffA (2 votes)")
	}
}

func writeCandidate(t *testing.T, dir, id string, k int, patch string) {
	t.Helper()
	d := filepath.Join(dir, "candidates", id)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(d, itoa(k)+".diff")
	if err := os.WriteFile(name, []byte(patch), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeSignals(t *testing.T, dir, id, js string) {
	t.Helper()
	d := filepath.Join(dir, "signals")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, id+".json"), []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
}

// itoa 是最小整型转字符串（避免测试引入 strconv 之外的依赖，可读）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
