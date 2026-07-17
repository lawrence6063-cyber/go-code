// 本文件把磁盘上的 scaffold 产物（候选补丁 + Docker 内跑出的信号）聚合为 []Candidate，喂给 Select。
// 布局见 SCAFFOLD_SPEC §5.2：
//
//	candidates/<instance_id>/<k>.diff   候选补丁
//	signals/<instance_id>.json          每候选的 applied/repro/regression 信号（可选；缺失则退化为纯投票）
//
// 读文件的聚合在此（评测层工具），纯选择逻辑在 scaffold.go 的 Select（守 §3.3 纯函数不变量）。
package scaffold

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// InstanceCandidates 是一个实例的全部候选（已合并信号），供批量选择。
type InstanceCandidates struct {
	InstanceID string      // SWE-bench 实例标识
	Candidates []Candidate // 该实例的 N 个候选补丁 + 信号
}

// candidateSignal 是 signals/<instance_id>.json 中单个候选的信号（字段名与 Python 选择 harness 对齐）。
type candidateSignal struct {
	Index        int  `json:"index"`
	Applied      bool `json:"applied"`
	HaveRepro    bool `json:"have_repro"`
	ReproPassed  bool `json:"repro_passed"`
	HaveRegress  bool `json:"have_regress"`
	RegressionOK bool `json:"regression_ok"`
}

// instanceSignals 是 signals/<instance_id>.json 的整体结构。
type instanceSignals struct {
	InstanceID string            `json:"instance_id"`
	Candidates []candidateSignal `json:"candidates"`
}

// LoadSignals 从 reader 反序列化一个实例的信号（供单测与聚合器复用）。
func LoadSignals(r io.Reader) (map[int]candidateSignal, error) {
	var sig instanceSignals
	if err := json.NewDecoder(r).Decode(&sig); err != nil {
		return nil, fmt.Errorf("decode signals: %w", err)
	}
	m := make(map[int]candidateSignal, len(sig.Candidates))
	for _, c := range sig.Candidates {
		m[c.Index] = c
	}
	return m, nil
}

// LoadArtifacts 扫描 scaffold 产物目录，返回每个实例的候选集合（按 instance_id 升序，确定性）。
// 无 signals 文件时该实例退化为纯投票（Applied=true、无测试信号）。空/纯空白补丁被跳过（无改动无意义）。
func LoadArtifacts(artifactDir string) ([]InstanceCandidates, error) {
	candRoot := filepath.Join(artifactDir, "candidates")
	entries, err := os.ReadDir(candRoot)
	if err != nil {
		return nil, fmt.Errorf("read candidates dir %s: %w", candRoot, err)
	}
	var out []InstanceCandidates
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		cands, err := loadInstance(artifactDir, id)
		if err != nil {
			return nil, err
		}
		if len(cands) > 0 {
			out = append(out, InstanceCandidates{InstanceID: id, Candidates: cands})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InstanceID < out[j].InstanceID })
	return out, nil
}

// loadInstance 读取单个实例的候选补丁与（可选）信号，合并为 []Candidate（按序号升序）。
func loadInstance(artifactDir, id string) ([]Candidate, error) {
	diffs, err := filepath.Glob(filepath.Join(artifactDir, "candidates", id, "*.diff"))
	if err != nil {
		return nil, fmt.Errorf("glob candidates for %s: %w", id, err)
	}
	sigs := readInstanceSignals(artifactDir, id)
	var cands []Candidate
	for _, path := range diffs {
		idx, ok := indexFromDiffName(path)
		if !ok {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read candidate %s: %w", path, err)
		}
		if strings.TrimSpace(string(data)) == "" {
			continue // 空补丁：agent 未做改动，跳过
		}
		cands = append(cands, buildCandidate(idx, string(data), sigs))
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].Index < cands[j].Index })
	return cands, nil
}

// buildCandidate 用信号（若有）填充候选；无信号时退化为纯投票（Applied=true，无测试信号）。
func buildCandidate(idx int, patch string, sigs map[int]candidateSignal) Candidate {
	if s, ok := sigs[idx]; ok {
		return Candidate{
			Index:        idx,
			Patch:        patch,
			Applied:      s.Applied,
			ReproPassed:  s.ReproPassed,
			RegressionOK: s.RegressionOK,
			HaveRepro:    s.HaveRepro,
			HaveRegress:  s.HaveRegress,
		}
	}
	return Candidate{Index: idx, Patch: patch, Applied: true}
}

// readInstanceSignals 读取 signals/<id>.json；不存在或解析失败返回空 map（退化为纯投票，不中断）。
func readInstanceSignals(artifactDir, id string) map[int]candidateSignal {
	f, err := os.Open(filepath.Join(artifactDir, "signals", id+".json"))
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	m, err := LoadSignals(f)
	if err != nil {
		return nil
	}
	return m
}

// indexFromDiffName 从 "<k>.diff" 文件名解析采样序号 k。
func indexFromDiffName(path string) (int, bool) {
	base := strings.TrimSuffix(filepath.Base(path), ".diff")
	n, err := strconv.Atoi(base)
	if err != nil {
		return 0, false
	}
	return n, true
}
