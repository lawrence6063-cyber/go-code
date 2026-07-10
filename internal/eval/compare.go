package eval

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// MetricDelta 是单个全局指标的基线 → 当前变化。
type MetricDelta struct {
	Name string  // 指标名
	Base float64 // 基线值
	Head float64 // 当前值
}

// Delta 返回值：Head 相对 Base 的差（Head - Base）。
func (d MetricDelta) Delta() float64 { return d.Head - d.Base }

// Comparison 是两份报告的回归对比结论（EVAL_SPEC §6.4：标红退化项，防「改好一个改坏三个」）。
type Comparison struct {
	Metrics   []MetricDelta // 全局核心指标 delta
	Regressed []string      // 基线通过、当前失败的 case id（退化，红线）
	Fixed     []string      // 基线失败、当前通过的 case id（改善）
}

// HasRegression 报告是否存在退化（有 case 从通过变失败）；供 --fail-on-regress 决定退出码。
func (c Comparison) HasRegression() bool { return len(c.Regressed) > 0 }

// Compare 对比基线与当前报告，产出全局指标 delta 与逐 case 通过态变化。
func Compare(base, head Report) Comparison {
	c := Comparison{Metrics: metricDeltas(base.Metrics, head.Metrics)}
	basePass := passByID(base.Cases)
	headPass := passByID(head.Cases)
	for id, bp := range basePass {
		hp, ok := headPass[id]
		if !ok {
			continue
		}
		switch {
		case bp && !hp:
			c.Regressed = append(c.Regressed, id)
		case !bp && hp:
			c.Fixed = append(c.Fixed, id)
		}
	}
	sort.Strings(c.Regressed)
	sort.Strings(c.Fixed)
	return c
}

// metricDeltas 组装核心全局指标的基线→当前对比行。
func metricDeltas(base, head Metrics) []MetricDelta {
	return []MetricDelta{
		{"Task Success Rate", base.SuccessRate, head.SuccessRate},
		{"Pass@1 Rate", base.Pass1Rate, head.Pass1Rate},
		{"Avg Iterations-to-Green", base.AvgIterationsToGreen, head.AvgIterationsToGreen},
		{"Avg Cost USD", base.AvgCostUSD, head.AvgCostUSD},
		{"Avg WallClock (s)", base.AvgWallClockSec, head.AvgWallClockSec},
	}
}

// passByID 把逐 case 的通过态索引为 id→pass。
func passByID(cases []CaseResult) map[string]bool {
	m := make(map[string]bool, len(cases))
	for _, c := range cases {
		m[c.ID] = c.Pass
	}
	return m
}

// WriteMarkdown 把对比结论写为人读 delta 报告（指标 delta 表 + 退化/改善清单）。
func (c Comparison) WriteMarkdown(w io.Writer) error {
	var b strings.Builder
	b.WriteString("# cogent eval compare\n\n## 指标 delta（Head − Base）\n\n")
	b.WriteString("| 指标 | Base | Head | Δ |\n| --- | --- | --- | --- |\n")
	for _, m := range c.Metrics {
		fmt.Fprintf(&b, "| %s | %.4f | %.4f | %+.4f |\n", m.Name, m.Base, m.Head, m.Delta())
	}
	writeIDList(&b, "## 退化样本（base 通过 → head 失败）", c.Regressed, "_(无退化)_")
	writeIDList(&b, "## 改善样本（base 失败 → head 通过）", c.Fixed, "_(无改善)_")
	_, err := io.WriteString(w, b.String())
	return err
}

// writeIDList 写一个带标题的 id 列表段（空列表输出占位）。
func writeIDList(b *strings.Builder, title string, ids []string, empty string) {
	fmt.Fprintf(b, "\n%s\n\n", title)
	if len(ids) == 0 {
		fmt.Fprintf(b, "%s\n", empty)
		return
	}
	for _, id := range ids {
		fmt.Fprintf(b, "- %s\n", id)
	}
}
