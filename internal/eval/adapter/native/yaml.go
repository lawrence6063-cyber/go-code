// 本文件实现 task.yaml 的极简手写解析器（EVAL_SPEC §5.4 / §8.5「先手写极简解析器守零依赖」）。
// task.yaml 字段少、结构平（仅一层 budget 嵌套 + 内联数组），无需引入 yaml 库。
package native

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"
)

// BudgetYAML 是 task.yaml 中 budget 块的机器映射（对应 loop.Budget 三重护栏）。
type BudgetYAML struct {
	MaxIterations int     // max_iterations
	MaxCostUSD    float64 // max_cost_usd
	MaxWallClock  string  // max_wallclock（如 "8m"，交由上层 time.ParseDuration）
}

// TaskYAML 是 eval/tasks/<name>/task.yaml 的机器映射（EVAL_SPEC §5.1）。
type TaskYAML struct {
	ID               string     // 任务稳定标识
	Difficulty       string     // easy | medium | hard
	Languages        []string   // 语言标签
	Capabilities     []string   // 维度标签
	Workdir          string     // repo | repo-root
	Budget           BudgetYAML // 预算护栏覆盖
	ExpectedOutcome  string     // achieved | budget_spent | canceled
	Verifier         string     // script | llm_judge
	Timeout          string     // 单任务墙钟硬上限（如 "3m"）
	Oracle           string     // 参考解路径，或 "n/a"
	SolvabilityCheck bool       // 是否纳入可解性双校验
}

// parseTaskYAML 解析 task.yaml：支持顶层标量、内联数组 [a, b] 与单层 budget 嵌套块。
// 容错优先：无法识别的键忽略，非法数值退化为零值，不返回错误（缺字段由上层用默认兜底）。
func parseTaskYAML(data []byte) TaskYAML {
	var y TaskYAML
	sc := bufio.NewScanner(bytes.NewReader(data))
	inBudget := false
	for sc.Scan() {
		line := stripComment(sc.Text())
		if strings.TrimSpace(line) == "" {
			continue
		}
		indented := line[0] == ' ' || line[0] == '\t'
		key, val := splitKV(line)
		if key == "" {
			continue
		}
		if inBudget && indented {
			applyBudget(&y.Budget, key, val)
			continue
		}
		inBudget = key == "budget"
		applyTop(&y, key, val)
	}
	return y
}

// applyTop 把一个顶层 key/value 写入 TaskYAML（budget 块由 caller 单独处理）。
func applyTop(y *TaskYAML, key, val string) {
	switch key {
	case "id":
		y.ID = unquote(val)
	case "difficulty":
		y.Difficulty = unquote(val)
	case "languages":
		y.Languages = parseList(val)
	case "capabilities":
		y.Capabilities = parseList(val)
	case "workdir":
		y.Workdir = unquote(val)
	case "expected_outcome":
		y.ExpectedOutcome = unquote(val)
	case "verifier":
		y.Verifier = unquote(val)
	case "timeout":
		y.Timeout = unquote(val)
	case "oracle":
		y.Oracle = unquote(val)
	case "solvability_check":
		y.SolvabilityCheck = unquote(val) == "true"
	}
}

// applyBudget 把一个 budget 子键写入 BudgetYAML；非法数值退化为零值。
func applyBudget(b *BudgetYAML, key, val string) {
	switch key {
	case "max_iterations":
		b.MaxIterations, _ = strconv.Atoi(unquote(val))
	case "max_cost_usd":
		b.MaxCostUSD, _ = strconv.ParseFloat(unquote(val), 64)
	case "max_wallclock":
		b.MaxWallClock = unquote(val)
	}
}

// stripComment 去除行内 " #" 之后的注释（值本身不含裸 # 时安全，符合 task.yaml 约定）。
func stripComment(line string) string {
	if i := strings.Index(line, " #"); i >= 0 {
		return line[:i]
	}
	if strings.HasPrefix(strings.TrimSpace(line), "#") {
		return ""
	}
	return line
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

// parseList 解析内联数组 "[a, b, c]"；非数组或空返回 nil。
func parseList(s string) []string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return nil
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
