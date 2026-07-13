package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// Report 是一次批量跑分的聚合结论（同时序列化为 report.md 人读 + report.json 供 compare）。
type Report struct {
	Suite       string             `json:"suite"`        // 套件名（如 native）
	Model       string             `json:"model"`        // 被测模型（可空）
	Concurrency int                `json:"concurrency"`  // 并发度
	GeneratedAt time.Time          `json:"generated_at"` // 生成时刻（UTC）
	Total       int                `json:"total"`        // 样本总数
	Metrics     Metrics            `json:"metrics"`      // 全局指标
	ByGroup     map[string]Metrics `json:"by_group"`     // 按 难度/语言/维度 分组聚合
	Cases       []CaseResult       `json:"cases"`        // 逐样本结局
}

// buildReport 从逐样本结果与运行选项聚合出 Report。
func buildReport(results []CaseResult, opts RunOptions) Report {
	suite := strings.TrimSpace(opts.Suite)
	if suite == "" {
		suite = "native"
	}
	return Report{
		Suite:       suite,
		Concurrency: opts.Concurrency,
		GeneratedAt: time.Now().UTC(),
		Total:       len(results),
		Metrics:     computeMetrics(results),
		ByGroup:     groupBy(results),
		Cases:       results,
	}
}

// WriteJSON 把 Report 写为机器可读 JSON（compare 的权威解析源，避免抓 Markdown 的脆弱性）。
func (r Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return fmt.Errorf("encode report json: %w", err)
	}
	return nil
}

// LoadReport 从 report.json 反序列化出 Report（供 eval compare 解析基线/当前报告）。
func LoadReport(r io.Reader) (Report, error) {
	var report Report
	if err := json.NewDecoder(r).Decode(&report); err != nil {
		return Report{}, fmt.Errorf("decode report json: %w", err)
	}
	return report, nil
}

// WriteMarkdown 把 Report 写为人读基线报告（EVAL_SPEC §6.7 模板：总览 + 分组交叉表 + 失败清单）。
func (r Report) WriteMarkdown(w io.Writer) error {
	var b strings.Builder
	r.writeHeader(&b)
	r.writeOverview(&b)
	r.writeGroups(&b)
	r.writeFailures(&b)
	_, err := io.WriteString(w, b.String())
	return err
}

// writeHeader 写报告标题与运行元信息。
func (r Report) writeHeader(b *strings.Builder) {
	fmt.Fprintf(b, "# cogent eval report — %s @ %s\n", r.Suite, r.GeneratedAt.Format(time.RFC3339))
	model := r.Model
	if model == "" {
		model = "(default)"
	}
	fmt.Fprintf(b, "model: %s  |  concurrency: %d  |  total: %d\n\n", model, r.Concurrency, r.Total)
}

// writeOverview 写全局核心指标总览表。
func (r Report) writeOverview(b *strings.Builder) {
	m := r.Metrics
	b.WriteString("## 总览\n\n| 指标 | 值 |\n| --- | --- |\n")
	fmt.Fprintf(b, "| Task Success Rate | %d/%d (%.1f%%) |\n", m.Passed, m.Total, m.SuccessRate*100)
	fmt.Fprintf(b, "| Pass@1 | %d/%d (%.1f%%) |\n", m.Pass1, m.Total, m.Pass1Rate*100)
	fmt.Fprintf(b, "| Avg Iterations-to-Green | %.2f |\n", m.AvgIterationsToGreen)
	fmt.Fprintf(b, "| Avg Cost USD | $%.4f |\n", m.AvgCostUSD)
	fmt.Fprintf(b, "| Avg WallClock | %.1fs |\n", m.AvgWallClockSec)
	fmt.Fprintf(b, "| Fatal Count | %d |\n\n", m.FatalCount)
}

// writeGroups 写「难度 × 语言 × 维度」分组交叉表（key 字典序稳定输出）。
func (r Report) writeGroups(b *strings.Builder) {
	b.WriteString("## 难度 × 语言 × 维度 交叉表\n\n| 分组 | 通过 | 数量 | 通过率 |\n| --- | --- | --- | --- |\n")
	keys := make([]string, 0, len(r.ByGroup))
	for k := range r.ByGroup {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		m := r.ByGroup[k]
		fmt.Fprintf(b, "| %s | %d | %d | %.1f%% |\n", k, m.Passed, m.Total, m.SuccessRate*100)
	}
	b.WriteString("\n")
}

// writeFailures 写失败样本清单（未 Pass 的样本，含结局与 artifact 链接）。
func (r Report) writeFailures(b *strings.Builder) {
	b.WriteString("## 失败样本\n\n| id | outcome | expected | iters | cost | artifact |\n| --- | --- | --- | --- | --- | --- |\n")
	var any bool
	for _, c := range r.Cases {
		if c.Pass {
			continue
		}
		any = true
		fmt.Fprintf(b, "| %s | %s | %s | %d | $%.4f | %s |\n",
			c.ID, c.Outcome.String(), c.ExpectedOutcome, c.Iterations, c.SpentUSD, c.ArtifactPath)
	}
	if !any {
		b.WriteString("| _(none)_ | | | | | |\n")
	}
	b.WriteString("\n")
}
