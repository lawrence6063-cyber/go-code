// 本文件定义 SWE-bench 单条样本（instance）的机器映射与 JSONL 加载器（EVAL_SPEC §5.2）。
//
// 数据集不随本仓库分发（守零依赖）：用户自 HuggingFace 导出 SWE-bench(_Lite) 为一个 JSONL 文件
// （每行一条 instance），Adapter 只读取本地文件、不联网拉取。SWE-bench 官方数据集把
// FAIL_TO_PASS / PASS_TO_PASS 存成「一个包含 JSON 数组的字符串」，故用 stringList 兼容
// 「JSON 数组」与「JSON 字符串里再套数组」两种写法。
package swebench

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Instance 是 SWE-bench 数据集的一条样本：真实仓库某次 issue 的修复任务。
// gold patch（Patch 字段）绝不喂给 agent，仅供离线可解性自证；TestPatch 是隐藏的判定测试，
// 判定时瞬态应用、判完还原，agent 全程看不到（EVAL_SPEC §5.2.1）。
type Instance struct {
	InstanceID       string     `json:"instance_id"`              // 样本稳定标识，如 "django__django-12345"
	Repo             string     `json:"repo"`                     // GitHub 仓库，如 "django/django"
	BaseCommit       string     `json:"base_commit"`              // 修复所基于的提交
	ProblemStatement string     `json:"problem_statement"`        // issue 文本（喂给 agent 的意图）
	Patch            string     `json:"patch"`                    // gold 参考解 patch（绝不喂 agent，仅 oracle 自证）
	TestPatch        string     `json:"test_patch"`               // 隐藏判定测试 patch（瞬态应用于判定）
	FailToPass       stringList `json:"FAIL_TO_PASS"`             // 修复后应由失败转通过的测试标识
	PassToPass       stringList `json:"PASS_TO_PASS"`             // 修复后应保持通过的测试标识（回归断言）
	EnvSetupCommit   string     `json:"environment_setup_commit"` // 环境搭建基准提交（本地判定信息用途）
	Version          string     `json:"version"`                  // 仓库版本标签
	TestCmd          string     `json:"test_cmd"`                 // 可选：cogent 扩展字段，显式指定测试命令（覆盖默认推导）
}

// Filter 按 instance_id / repo 筛选，并可限制取样数量（个人项目取 Lite 子集，EVAL_SPEC §5.2.3）。
type Filter struct {
	InstanceIDs []string // 只跑这些 instance_id（空=不限）
	Repos       []string // 只跑这些 repo（空=不限）
	Limit       int      // 最多取 N 条（<=0=不限）
}

// stringList 兼容两种 JSON 写法：直接的数组 ["a","b"]，或字符串里再套数组 "[\"a\",\"b\"]"
// （SWE-bench HuggingFace 导出常用后者）。
type stringList []string

// UnmarshalJSON 见 encoding/json：先按数组解析，失败再按「字符串包裹的数组」解析。
func (s *stringList) UnmarshalJSON(data []byte) error {
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		*s = arr
		return nil
	}
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return fmt.Errorf("stringList: neither array nor string: %w", err)
	}
	str = strings.TrimSpace(str)
	if str == "" {
		*s = nil
		return nil
	}
	if err := json.Unmarshal([]byte(str), &arr); err != nil {
		return fmt.Errorf("stringList: embedded array parse: %w", err)
	}
	*s = arr
	return nil
}

// LoadInstances 读取 JSONL 数据集文件（每行一条 instance），按 filter 过滤后返回。
// 空行与以 '#' 起始的注释行跳过；单行解析失败即 fail-fast 返回错误（数据集损坏应显式暴露）。
func LoadInstances(file string, f Filter) ([]Instance, error) {
	fh, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("open dataset %s: %w", file, err)
	}
	defer func() { _ = fh.Close() }()
	return parseInstances(fh, f)
}

// parseInstances 从 reader 逐行解析并过滤 instance（与 LoadInstances 分离以便单测注入）。
func parseInstances(r io.Reader, f Filter) ([]Instance, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // instance 含 patch，单行可能较大
	var out []Instance
	for line := 1; sc.Scan(); line++ {
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 || raw[0] == '#' {
			continue
		}
		var inst Instance
		if err := json.Unmarshal(raw, &inst); err != nil {
			return nil, fmt.Errorf("parse dataset line %d: %w", line, err)
		}
		if matches(inst, f) {
			out = append(out, inst)
			if f.Limit > 0 && len(out) >= f.Limit {
				break
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan dataset: %w", err)
	}
	return out, nil
}

// matches 报告 instance 是否命中筛选（每类 OR、跨类 AND；空类不限）。
func matches(inst Instance, f Filter) bool {
	return anyEqual(f.InstanceIDs, inst.InstanceID) && anyEqual(f.Repos, inst.Repo)
}

// anyEqual 报告 want 为空（不限）或 have 命中 want 中任一项（大小写不敏感）。
func anyEqual(want []string, have string) bool {
	if len(want) == 0 {
		return true
	}
	for _, w := range want {
		if strings.EqualFold(strings.TrimSpace(w), have) {
			return true
		}
	}
	return false
}
