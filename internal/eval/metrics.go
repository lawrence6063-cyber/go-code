package eval

import "github.com/alaindong/cogent/internal/loop"

// Metrics 是一组样本（全局或某分组）的聚合指标（EVAL_SPEC §6.6 首版可算子集）。
// 全部可仅从 CaseResult / LoopResult 导出；Review/Convergence Curve 等需执行体额外透出事件，
// 列为增量，首版不算。
type Metrics struct {
	Total                int     `json:"total"`                   // 样本数
	Passed               int     `json:"passed"`                  // 判 Pass 的样本数
	SuccessRate          float64 `json:"success_rate"`            // Passed / Total
	Pass1                int     `json:"pass_at_1"`               // Pass 且首轮即达（Iterations==1）
	Pass1Rate            float64 `json:"pass_at_1_rate"`          // Pass1 / Total
	AvgIterationsToGreen float64 `json:"avg_iterations_to_green"` // 达标样本（Pass 且 achieved）的平均收敛轮数
	AvgCostUSD           float64 `json:"avg_cost_usd"`            // 平均成本
	AvgWallClockSec      float64 `json:"avg_wallclock_sec"`       // 平均端到端耗时（秒）
	FatalCount           int     `json:"fatal_count"`             // 执行/判定过程错误数（OutcomeFatal）
}

// computeMetrics 从一组 CaseResult 计算聚合指标。空集合返回零值 Metrics。
func computeMetrics(results []CaseResult) Metrics {
	m := Metrics{Total: len(results)}
	if m.Total == 0 {
		return m
	}
	var greenIters, greenCount int
	var costSum, wallSum float64
	for _, r := range results {
		if r.Pass {
			m.Passed++
			if r.Iterations == 1 {
				m.Pass1++
			}
		}
		if r.Pass && r.Outcome == loop.OutcomeAchieved {
			greenIters += r.Iterations
			greenCount++
		}
		if r.Outcome == loop.OutcomeFatal {
			m.FatalCount++
		}
		costSum += r.SpentUSD
		wallSum += r.Elapsed.Seconds()
	}
	m.SuccessRate = ratio(m.Passed, m.Total)
	m.Pass1Rate = ratio(m.Pass1, m.Total)
	if greenCount > 0 {
		m.AvgIterationsToGreen = float64(greenIters) / float64(greenCount)
	}
	m.AvgCostUSD = float64(costSum) / float64(m.Total)
	m.AvgWallClockSec = float64(wallSum) / float64(m.Total)
	return m
}

// groupBy 按 难度 / 语言 / 维度 把样本分桶，返回 key→Metrics（key 如 "difficulty=easy"、
// "language=go"、"capability=budget"），供报告输出「难度 × 语言 × 维度」交叉表。
func groupBy(results []CaseResult) map[string]Metrics {
	buckets := map[string][]CaseResult{}
	add := func(key string, r CaseResult) { buckets[key] = append(buckets[key], r) }
	for _, r := range results {
		if r.Meta.Difficulty != "" {
			add("difficulty="+r.Meta.Difficulty, r)
		}
		for _, l := range r.Meta.Languages {
			add("language="+l, r)
		}
		for _, c := range r.Meta.Capabilities {
			add("capability="+c, r)
		}
	}
	out := make(map[string]Metrics, len(buckets))
	for k, rs := range buckets {
		out[k] = computeMetrics(rs)
	}
	return out
}

// ratio 计算 n/d，d==0 时返回 0（避免 NaN）。
func ratio(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}
