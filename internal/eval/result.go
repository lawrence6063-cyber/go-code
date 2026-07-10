package eval

import (
	"encoding/json"
	"time"

	"github.com/alaindong/cogent/internal/eval/adapter"
	"github.com/alaindong/cogent/internal/loop"
)

// CaseResult 是单条样本的执行结局（Report.Cases 元素；也序列化进 report.json）。
type CaseResult struct {
	ID              string        // 样本稳定标识
	Meta            adapter.Meta  // 分组标签
	Outcome         loop.Outcome  // 实际结局（achieved/budget_spent/canceled/fatal）
	ExpectedOutcome string        // 期望结局（native 来自 task.yaml；外部基准默认 achieved）
	Pass            bool          // 是否「评测通过」——按判定矩阵（§6.5），非简单看 verify
	Iterations      int           // 实际轮数（LoopResult.Iterations）
	SpentUSD        float64       // 累计成本（LoopResult.SpentUSD）
	Elapsed         time.Duration // 端到端耗时
	VerifyPassed    bool          // 最后一次判定是否通过（LoopResult.LastReport.Passed）
	ArtifactPath    string        // 该 case 的 artifact 目录
	Err             string        // 执行/判定过程错误（对应 OutcomeFatal；区别于「目标未达成」）
}

// judgePass 按 expected_outcome 分流判定单条样本是否「评测通过」（EVAL_SPEC §6.5 判定矩阵）：
//   - achieved   ：Outcome==OutcomeAchieved（verify 最终通过）；
//   - budget_spent：Outcome==OutcomeBudgetSpent 且未超轮（反向评测——系统「正确地失败并早停」）；
//   - canceled   ：Outcome==OutcomeCanceled；
//
// OutcomeFatal 一律 false。
func judgePass(expected string, budget loop.Budget, r loop.LoopResult) bool {
	switch expected {
	case "", "achieved":
		return r.Outcome == loop.OutcomeAchieved
	case "budget_spent":
		if r.Outcome != loop.OutcomeBudgetSpent {
			return false
		}
		return budget.MaxIterations <= 0 || r.Iterations <= budget.MaxIterations
	case "canceled":
		return r.Outcome == loop.OutcomeCanceled
	default:
		return false
	}
}

// caseResultJSON 是 CaseResult 的稳定 JSON 视图：Outcome 以字符串、耗时以秒呈现，
// 供 report.json 人读且供 eval compare 稳定解析（避免 loop.Outcome int 枚举漂移）。
type caseResultJSON struct {
	ID              string   `json:"id"`
	Difficulty      string   `json:"difficulty,omitempty"`
	Languages       []string `json:"languages,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	Source          string   `json:"source,omitempty"`
	Outcome         string   `json:"outcome"`
	ExpectedOutcome string   `json:"expected_outcome"`
	Pass            bool     `json:"pass"`
	Iterations      int      `json:"iterations"`
	SpentUSD        float64  `json:"spent_usd"`
	ElapsedSec      float64  `json:"elapsed_sec"`
	VerifyPassed    bool     `json:"verify_passed"`
	ArtifactPath    string   `json:"artifact_path,omitempty"`
	Err             string   `json:"err,omitempty"`
}

// MarshalJSON 见 caseResultJSON 说明：输出稳定、人读、可 compare 的 JSON 形态。
func (r CaseResult) MarshalJSON() ([]byte, error) {
	return json.Marshal(caseResultJSON{
		ID:              r.ID,
		Difficulty:      r.Meta.Difficulty,
		Languages:       r.Meta.Languages,
		Capabilities:    r.Meta.Capabilities,
		Source:          r.Meta.Source,
		Outcome:         r.Outcome.String(),
		ExpectedOutcome: r.ExpectedOutcome,
		Pass:            r.Pass,
		Iterations:      r.Iterations,
		SpentUSD:        r.SpentUSD,
		ElapsedSec:      r.Elapsed.Seconds(),
		VerifyPassed:    r.VerifyPassed,
		ArtifactPath:    r.ArtifactPath,
		Err:             r.Err,
	})
}

// UnmarshalJSON 从稳定 JSON 视图还原 CaseResult（供 eval compare 回读 report.json）。
func (r *CaseResult) UnmarshalJSON(data []byte) error {
	var j caseResultJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return err
	}
	r.ID = j.ID
	r.Meta = adapter.Meta{
		Difficulty: j.Difficulty, Languages: j.Languages,
		Capabilities: j.Capabilities, Source: j.Source,
	}
	r.Outcome = parseOutcome(j.Outcome)
	r.ExpectedOutcome = j.ExpectedOutcome
	r.Pass = j.Pass
	r.Iterations = j.Iterations
	r.SpentUSD = j.SpentUSD
	r.Elapsed = time.Duration(j.ElapsedSec * float64(time.Second))
	r.VerifyPassed = j.VerifyPassed
	r.ArtifactPath = j.ArtifactPath
	r.Err = j.Err
	return nil
}

// parseOutcome 把结局字符串映射回 loop.Outcome；未知值 fail-closed 归为 fatal。
func parseOutcome(s string) loop.Outcome {
	switch s {
	case "achieved":
		return loop.OutcomeAchieved
	case "budget_spent":
		return loop.OutcomeBudgetSpent
	case "canceled":
		return loop.OutcomeCanceled
	default:
		return loop.OutcomeFatal
	}
}
