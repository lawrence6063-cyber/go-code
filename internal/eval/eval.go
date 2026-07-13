// Package eval 是 EVAL_SPEC §6 的 Headless 批量跑分运行器：把「跑一批 Case → 按判定矩阵
// 填 CaseResult → 聚合指标 → 出报告」标准化。它复用 loop.Orchestrator 作为每个样本的执行体，
// 但执行体的装配（Engine/Pipeline/Cost/Tracer）经注入式 Executor 由 cmd 层提供——runner 因此
// 无需 import engine/agent，守 EVAL_SPEC §5.3 依赖方向（评测层只 import 内核，绝不被内核 import）。
//
// 支持顺序（Concurrency<=1）与并发 worker 池（Concurrency>1）两种执行；ctx 取消即停止派发新样本、
// 等在途样本收尾并归档已完成结果（无 goroutine 泄漏）。用 fake Executor 即可对 loader / 判定矩阵 /
// 指标聚合 / 报告序列化做纯单测（无需真实 LLM）。
package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alaindong/cogent/internal/eval/adapter"
	"github.com/alaindong/cogent/internal/loop"
)

// Executor 把一条 Case 执行成一次目标循环的最终结局；由 cmd/cogent 注入，内部复用
// buildOrchestrator/buildVerifier 装配 Engine/Pipeline，并负责 drain RunGoal 的事件流到
// LoopFinished。测试可注入 fake，返回预置 LoopResult，免真实 LLM/网络。
type Executor interface {
	// Run 执行一条 case；art 为该 case 的 artifact 目录（写 trace/progress/transcript）。
	Run(ctx context.Context, c adapter.Case, art string) (loop.LoopResult, error)
}

// RunOptions 控制执行体、并发、预算覆盖与归档。
type RunOptions struct {
	Executor      Executor    // 执行体（cmd 注入；测试注入 fake）
	Suite         string      // 套件名（报告标题用，如 native | polyglot；空=native）
	Concurrency   int         // 并发样本数（<=1 顺序执行；>1 启 worker 池）
	Budget        loop.Budget // 全局预算覆盖（零值不覆盖，用 case 自带 / DefaultBudget）
	ArtifactDir   string      // trace / progress / transcript / workspace 归档根目录
	KeepWorkspace bool        // 跑完是否保留 case 工作区副本（默认由 caller 决定）
}

// Runner 批量执行被测样本并聚合指标（EVAL_SPEC §6）。
type Runner interface {
	// Run 执行样本集合，返回聚合结果；ctx 取消即安全收尾并返回已完成样本的报告（无泄漏）。
	Run(ctx context.Context, cases []adapter.Case, opts RunOptions) (Report, error)
}

// runner 是默认 Runner 实现，支持顺序与并发 worker 池两种执行模式。
type runner struct{}

// NewRunner 构造批量跑分运行器（顺序或并发由 RunOptions.Concurrency 决定）。
func NewRunner() Runner { return runner{} }

// preparedCase 是应用了预算覆盖并建好 artifact 目录的待执行样本。
type preparedCase struct {
	c   adapter.Case
	art string
}

// Run 见 Runner 接口说明：准备样本 → 顺序或并发执行 → 按判定矩阵聚合为 Report。
func (runner) Run(ctx context.Context, cases []adapter.Case, opts RunOptions) (Report, error) {
	if opts.Executor == nil {
		return Report{}, errors.New("eval: RunOptions.Executor is nil")
	}
	prepared, err := prepareCases(cases, opts)
	if err != nil {
		return buildReport(nil, opts), err
	}
	var results []CaseResult
	if opts.Concurrency > 1 {
		results = runConcurrent(ctx, opts.Executor, prepared, opts.Concurrency)
	} else {
		results = runSequential(ctx, opts.Executor, prepared)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ID < results[j].ID })
	return buildReport(results, opts), ctx.Err()
}

// prepareCases 逐样本应用全局预算覆盖并建 artifact 目录（IO 错误 fail-fast）。
func prepareCases(cases []adapter.Case, opts RunOptions) ([]preparedCase, error) {
	out := make([]preparedCase, 0, len(cases))
	for _, c := range cases {
		if hasBudgetOverride(opts.Budget) {
			c.Goal.Budget = opts.Budget
		}
		art, err := caseArtifactDir(opts.ArtifactDir, c.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, preparedCase{c: c, art: art})
	}
	return out, nil
}

// runSequential 顺序执行所有样本；ctx 取消即停止并返回已完成结果。
func runSequential(ctx context.Context, ex Executor, prepared []preparedCase) []CaseResult {
	results := make([]CaseResult, 0, len(prepared))
	for _, p := range prepared {
		if ctx.Err() != nil {
			break
		}
		results = append(results, runCase(ctx, ex, p.c, p.art))
	}
	return results
}

// runConcurrent 用固定大小 worker 池并发执行样本；ctx 取消即停止派发、等在途收尾（无泄漏）。
// 结果经互斥收集后由调用方排序，保证报告确定性。
func runConcurrent(ctx context.Context, ex Executor, prepared []preparedCase, conc int) []CaseResult {
	jobs := make(chan preparedCase)
	results := make([]CaseResult, 0, len(prepared))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for w := 0; w < conc; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				r := runCase(ctx, ex, p.c, p.art)
				mu.Lock()
				results = append(results, r)
				mu.Unlock()
			}
		}()
	}
dispatch:
	for _, p := range prepared {
		select {
		case <-ctx.Done():
			break dispatch
		case jobs <- p:
		}
	}
	close(jobs)
	wg.Wait()
	return results
}

// runCase 执行单条 case：套 per-case 墙钟硬上限 → 调 Executor → 归一为 CaseResult（含判 Pass）。
func runCase(ctx context.Context, ex Executor, c adapter.Case, art string) CaseResult {
	cctx := ctx
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		cctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	start := time.Now()
	res, err := ex.Run(cctx, c, art)
	cr := CaseResult{
		ID:              c.ID,
		Meta:            c.Meta,
		ExpectedOutcome: c.ExpectedOutcome,
		Elapsed:         time.Since(start),
		ArtifactPath:    art,
	}
	if err != nil {
		cr.Outcome = loop.OutcomeFatal
		cr.Err = err.Error()
		archiveCase(cr)
		return cr
	}
	cr.Outcome = res.Outcome
	cr.Iterations = res.Iterations
	cr.SpentUSD = res.SpentUSD
	cr.VerifyPassed = res.LastReport.Passed
	cr.Pass = judgePass(c.ExpectedOutcome, c.Goal.Budget, res)
	archiveCase(cr)
	return cr
}

// archiveCase 把单条 case 结局写为 <art>/result.json，供失败样本复盘（art 为空则跳过）。
// 写失败仅静默忽略（归档是尽力而为，不影响跑分结果）。
func archiveCase(cr CaseResult) {
	if strings.TrimSpace(cr.ArtifactPath) == "" {
		return
	}
	data, err := json.MarshalIndent(cr, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(cr.ArtifactPath, "result.json"), data, 0o644)
}

// caseArtifactDir 为 case 建 artifact 子目录（ArtifactDir 为空则不建，返回空路径）。
func caseArtifactDir(root, caseID string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", nil
	}
	art := filepath.Join(root, sanitizeID(caseID))
	if err := os.MkdirAll(art, 0o755); err != nil {
		return "", fmt.Errorf("mkdir artifact dir: %w", err)
	}
	return art, nil
}

// sanitizeID 把 case id 里的路径分隔符换成下划线，作为安全的目录名（如 native/x → native_x）。
func sanitizeID(id string) string {
	return strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(id)
}

// hasBudgetOverride 报告全局预算是否有任一非零字段（有则覆盖 case 自带预算）。
func hasBudgetOverride(b loop.Budget) bool {
	return b.MaxIterations > 0 || b.MaxCostUSD > 0 || b.MaxWallClock > 0
}
