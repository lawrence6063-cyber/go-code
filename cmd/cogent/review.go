package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/alaindong/cogent/internal/agent"
	"github.com/alaindong/cogent/internal/engine"
	"github.com/alaindong/cogent/internal/llm"
	"github.com/alaindong/cogent/internal/loop"
	"github.com/alaindong/cogent/internal/memory"
	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/sandbox"
	"github.com/alaindong/cogent/internal/verify"
	"github.com/alaindong/cogent/internal/worktree"
)

// gitDiscarder 在审查未通过时经沙箱回滚工作区改动（「通过才落盘」的 diff 暂存版，L2）。
// 用白名单 git 命令丢弃未提交改动；沙箱仍施加危险命令拦截与工作目录约束。
type gitDiscarder struct {
	sb sandbox.Sandbox
}

// Discard 见 agent.Discarder 接口说明。
func (g gitDiscarder) Discard(ctx context.Context) error {
	if _, err := g.sb.Exec(ctx, "git checkout -- . && git clean -fd"); err != nil {
		return fmt.Errorf("git discard: %w", err)
	}
	return nil
}

// pipelineAdapter 把 agent.MakerReviewer 适配为 loop.Pipeline（消费侧最小接口），
// 使 loop 包无需 import agent——依赖更干净，符合项目「消费侧定义接口」惯例。
// 设计动机（OPTIMIZE_SPEC A2）：LOOP_SPEC §3.2 本允许 loop→agent，但选择在 cmd 层用适配器桥接，
// 换取 loop 对 agent 的零编译依赖（与 engineRunner/Spawner 惯例一致），是有意识的解耦权衡而非设计失误。
// 落盘闸门：verifyAt 非 nil 时由客观 verify 决定保留/回滚（reviewer 降级为建议）；为 nil 时回退 reviewer 裁决。
type pipelineAdapter struct {
	mr        *agent.MakerReviewer
	workRoot  string          // maker 改动所在工作根，供 verifyAt 在其上执行客观判定
	discarder agent.Discarder // verify/裁决未通过时回滚工作区改动；为 nil 不回滚
}

// Iterate 见 loop.Pipeline 接口说明：maker/reviewer → 客观 verify 硬闸门 → 通过保留 / 否则回滚。
func (a pipelineAdapter) Iterate(ctx context.Context, task string, verifyAt loop.VerifyAtFunc) (loop.PipelineResult, error) {
	r, err := a.mr.Iterate(ctx, task)
	if err != nil {
		return loop.PipelineResult{}, err
	}
	if verifyAt != nil {
		report := verifyAt(ctx, a.workRoot)
		if !report.Passed {
			a.discard(ctx) // 客观未通过：回滚 maker 改动，主工作区零残留
			return loop.PipelineResult{
				Summary:  r.MakerSummary,
				Approved: r.Verdict.Approved,
				Feedback: r.Verdict.Feedback,
				Report:   &report,
			}, nil
		}
		return loop.PipelineResult{Summary: r.MakerSummary, Approved: true, Report: &report}, nil
	}
	if !r.Verdict.Approved {
		a.discard(ctx) // 无客观判据：回退 reviewer 裁决作为落盘闸门（向后兼容）
		return loop.PipelineResult{Summary: r.MakerSummary, Approved: false, Feedback: r.Verdict.Feedback}, nil
	}
	return loop.PipelineResult{Summary: r.MakerSummary, Approved: true}, nil
}

// discard 在未通过时回滚 maker 改动；无 discarder 则不操作，回滚失败仅告警不阻断续跑。
func (a pipelineAdapter) discard(ctx context.Context) {
	if a.discarder == nil {
		return
	}
	if err := a.discarder.Discard(ctx); err != nil {
		slog.Warn("discard rejected changes failed", "err", err)
	}
}

// makerReviewerRunner 抽象 worktree 内执行的「一轮双角色」（maker 改 + reviewer 审），
// 便于对 worktreePipeline 的落盘分支逻辑做注入式测试；*agent.MakerReviewer 隐式满足。
type makerReviewerRunner interface {
	Iterate(ctx context.Context, task string) (agent.PipelineResult, error)
}

// worktreePipeline 是「通过才落盘」的 worktree 暂存版（L4-2）：每轮在独立 git worktree 内
// 让 maker 改、reviewer 审同一 worktree，审查通过则 Merge 回基线、否则 Discard 整个 worktree。
// 相比 diff 回滚，物理隔离更干净（无「回滚不彻底」风险），且天然支持多 maker 并行。
// 合并冲突降级为「本轮未通过」并附说明，交上层带反馈续跑（最终撞预算时由 daemon 落为 Blocked）。
type worktreePipeline struct {
	mgr     worktree.Manager
	baseRef string                                    // worktree 派生与合并回的基线引用
	build   func(workRoot string) makerReviewerRunner // 按 worktree 根重建双角色（无 discarder）
	tracer  observe.Tracer                            // worktree.* span 埋点；nil 时退化为 no-op（守依赖方向：worktree 叶子包不依赖 observe）
}

// span 在 worktreePipeline 上开启一个 worktree.* span；tracer 为 nil 时返回 no-op 结束函数（兼容测试）。
func (p *worktreePipeline) span(ctx context.Context, name string, attrs ...observe.Attr) (context.Context, observe.EndFunc) {
	if p.tracer == nil {
		return ctx, func(error, ...observe.Attr) {}
	}
	return p.tracer.Start(ctx, name, attrs...)
}

// Iterate 见 loop.Pipeline 接口说明：Create → maker/reviewer → 客观 verify 硬闸门 → 通过 Merge / 否则 Discard。
// verifyAt 非 nil 时在 worktree 根执行客观判定，verify 通过才 Merge 落盘（reviewer 降级为建议，不可短路）；
// 为 nil 时回退到 reviewer 裁决作为落盘闸门（向后兼容）。
func (p *worktreePipeline) Iterate(ctx context.Context, task string, verifyAt loop.VerifyAtFunc) (loop.PipelineResult, error) {
	ws, err := p.create(ctx)
	if err != nil {
		return loop.PipelineResult{}, fmt.Errorf("create worktree: %w", err)
	}
	r, runErr := p.build(ws.Root).Iterate(ctx, task)
	if runErr != nil {
		p.discard(ctx, ws)
		return loop.PipelineResult{}, runErr
	}
	approved := r.Verdict.Approved
	feedback := r.Verdict.Feedback
	var report *verify.Report
	if verifyAt != nil { // 客观判据为最终硬闸门：verify 通过即落盘，reviewer 降级为建议
		rep := verifyAt(ctx, ws.Root)
		report = &rep
		approved = rep.Passed
		if !rep.Passed {
			feedback = combineFeedback(r.Verdict.Feedback, rep)
		}
	}
	if !approved {
		p.discard(ctx, ws) // 未通过：物理丢弃，主工作区零残留
		return loop.PipelineResult{Summary: r.MakerSummary, Approved: false, Feedback: feedback, Report: report}, nil
	}
	if err := p.merge(ctx, ws); err != nil {
		p.discard(ctx, ws)
		if errors.Is(err, worktree.ErrMergeConflict) {
			return loop.PipelineResult{
				Summary:  r.MakerSummary,
				Approved: false,
				Feedback: "merge conflict while landing changes: " + err.Error(),
				Report:   report,
			}, nil
		}
		return loop.PipelineResult{}, fmt.Errorf("merge worktree: %w", err)
	}
	return loop.PipelineResult{Summary: r.MakerSummary, Approved: true, Report: report}, nil
}

// combineFeedback 把客观 verify 摘要与 reviewer 建议合并为续跑反馈（verify 未通过时）。
func combineFeedback(reviewerFeedback string, report verify.Report) string {
	fb := strings.TrimSpace(report.Summary)
	if d := strings.TrimSpace(report.Detail); d != "" {
		fb = strings.TrimSpace(fb + "\n\n" + d)
	}
	if rf := strings.TrimSpace(reviewerFeedback); rf != "" {
		fb = strings.TrimSpace(fb + "\n\nreviewer feedback: " + rf)
	}
	return fb
}

// create 在 worktree.create span 下派生隔离工作区，并把分支名作为 span 属性。
func (p *worktreePipeline) create(ctx context.Context) (worktree.Workspace, error) {
	ctx, end := p.span(ctx, "worktree.create")
	ws, err := p.mgr.Create(ctx, p.baseRef)
	end(err, observe.Attr{Key: "worktree.branch", Value: ws.Branch})
	return ws, err
}

// merge 在 worktree.merge span 下把已审核通过的 worktree 合并回基线，并标注是否冲突。
func (p *worktreePipeline) merge(ctx context.Context, ws worktree.Workspace) error {
	ctx, end := p.span(ctx, "worktree.merge", observe.Attr{Key: "worktree.branch", Value: ws.Branch})
	err := p.mgr.Merge(ctx, ws, p.baseRef)
	end(err, observe.Attr{Key: "merge.conflict", Value: errors.Is(err, worktree.ErrMergeConflict)})
	return err
}

// discard 在 worktree.discard span 下物理丢弃 worktree；回滚失败仅记入 span，不阻断续跑。
func (p *worktreePipeline) discard(ctx context.Context, ws worktree.Workspace) {
	ctx, end := p.span(ctx, "worktree.discard", observe.Attr{Key: "worktree.branch", Value: ws.Branch})
	end(p.mgr.Discard(ctx, ws))
}

// buildMakerReviewer 按指定工作根装配 maker/reviewer 双角色：maker（可写池 + Auto）+
// reviewer（只读池 + Ask）。maker 与 reviewer 可指不同模型（COGENT_REVIEWER_MODEL 覆盖）。
// discarder 为 nil 时不回滚（worktree 暂存模式由 Manager 负责清理）。
func buildMakerReviewer(
	llmc llm.Client,
	prov observe.Provider,
	prompter permission.Prompter,
	workRoot string,
	discarder agent.Discarder,
	maxSteps int,
) *agent.MakerReviewer {
	model := os.Getenv("COGENT_MODEL")
	makerDeps := engine.Deps{
		LLM:      llmc,
		Tools:    buildMakerPool(workRoot, prompter, prov.Tracer()),
		Memory:   memory.New(),
		Observe:  prov,
		Mode:     engine.ModeAuto,
		Model:    model,
		WorkRoot: workRoot,
		MaxSteps: resolveMaxSteps(maxSteps),
	}
	reviewerDeps := engine.Deps{
		LLM:      llmc,
		Tools:    buildReviewerPool(workRoot),
		Observe:  prov,
		Mode:     engine.ModeAsk,
		Model:    reviewerModel(model),
		WorkRoot: workRoot,
		MaxSteps: resolveMaxSteps(maxSteps),
	}
	return agent.NewMakerReviewer(makerDeps, reviewerDeps, discarder)
}

// buildPipeline 装配 maker/reviewer 双角色流水线（diff 回滚版）：maker 改工作区，reviewer 审，
// 客观 verify 作为落盘硬闸门（未通过经 git 回滚）。MakerReviewer 不持有 discarder——
// 回滚时机改由 adapter 在客观 verify 之后决定，使 reviewer 不再能短路客观判据。
func buildPipeline(prov observe.Provider, prompter permission.Prompter, workRoot string, maxSteps int) (loop.Pipeline, error) {
	llmc, err := newLLMClient()
	if err != nil {
		return nil, err
	}
	sb := sandbox.New(sandbox.Config{WorkRoot: workRoot, Enabled: false})
	mr := buildMakerReviewer(llmc, prov, prompter, workRoot, nil, maxSteps)
	return pipelineAdapter{mr: mr, workRoot: workRoot, discarder: gitDiscarder{sb: sb}}, nil
}

// buildWorktreePipeline 装配 worktree 暂存版双角色流水线（L4-2）：每轮在隔离 worktree 内执行，
// 通过才 Merge 落盘。maker/reviewer 在 worktree 根上重建，物理隔离主工作区。
func buildWorktreePipeline(prov observe.Provider, prompter permission.Prompter, workRoot string, maxSteps int) (loop.Pipeline, error) {
	llmc, err := newLLMClient()
	if err != nil {
		return nil, err
	}
	sb := sandbox.New(sandbox.Config{WorkRoot: workRoot, Enabled: false})
	mgr := worktree.New(sb)
	return &worktreePipeline{
		mgr:     mgr,
		baseRef: "HEAD",
		tracer:  prov.Tracer(),
		build: func(root string) makerReviewerRunner {
			return buildMakerReviewer(llmc, prov, prompter, root, nil, maxSteps) // worktree 负责清理，无需 discarder
		},
	}, nil
}

// defaultReviewerModel 是审查者的缺省模型：选用指令遵循更稳的对话模型，
// 避免 reasoner 输出思维链导致裁决格式不合规而被 fail-closed 恒拒（发现③/④）。
const defaultReviewerModel = "deepseek-chat"

// reviewerModel 返回审查者使用的模型：优先 COGENT_REVIEWER_MODEL，否则用缺省对话模型
// （与 maker 模型解耦——maker 可用 reasoner 强推理，reviewer 用 chat 稳遵循裁决格式）。
func reviewerModel(string) string {
	if m := os.Getenv("COGENT_REVIEWER_MODEL"); m != "" {
		return m
	}
	return defaultReviewerModel
}

// buildOrchestrator 装配目标循环编排器：
//   - review + worktree：worktree 暂存版双角色流水线（通过才 Merge 落盘，物理隔离）；
//   - review：diff 回滚版双角色流水线（通过才落盘，未通过 git 回滚）；
//   - 否则：单 engine 执行体（含 MCP 工具）。
//
// 返回的 cleanup 用于释放可能持有的 MCP 连接（pipeline 模式不连接 MCP，返回空清理）。
func buildOrchestrator(
	ctx context.Context,
	prov observe.Provider,
	prompter permission.Prompter,
	mode engine.Mode,
	sessionID, workRoot string,
	review, useWorktree bool,
	maxSteps int,
) (loop.Orchestrator, func(), error) {
	noop := func() {}
	// 装饰 provider 注入成本计量：拦截 cogent.tokens 累计美元成本，并作为 loop.CostMeter 接入护栏，
	// 使 --max-cost 真正生效（修复发现⑤：成本护栏此前是空接线）。
	costProv := newCostProvider(prov)
	prov = costProv
	if review || useWorktree {
		pipeline, err := pipelineFor(prov, prompter, workRoot, useWorktree, maxSteps)
		if err != nil {
			return nil, noop, err
		}
		orch, err := loop.New(loop.Deps{
			Pipeline: pipeline, Tracer: prov.Tracer(), Meter: prov.Meter(), Cost: costProv.meter,
		})
		if err != nil {
			return nil, noop, fmt.Errorf("init loop: %w", err)
		}
		return orch, noop, nil
	}
	mgr, err := buildMCPManager(ctx, workRoot, prov.Tracer())
	if err != nil {
		return nil, noop, err
	}
	eng, err := buildEngine(prov, prompter, mode, sessionID, workRoot, mgr.Tools(), maxSteps)
	if err != nil {
		_ = mgr.Close()
		return nil, noop, err
	}
	orch, err := loop.New(loop.Deps{
		Engine: eng, Tracer: prov.Tracer(), Meter: prov.Meter(), Cost: costProv.meter,
	})
	if err != nil {
		_ = mgr.Close()
		return nil, noop, fmt.Errorf("init loop: %w", err)
	}
	return orch, func() { _ = mgr.Close() }, nil
}

// pipelineFor 按是否启用 worktree 选择双角色流水线的落盘策略。
func pipelineFor(prov observe.Provider, prompter permission.Prompter, workRoot string, useWorktree bool, maxSteps int) (loop.Pipeline, error) {
	if useWorktree {
		return buildWorktreePipeline(prov, prompter, workRoot, maxSteps)
	}
	return buildPipeline(prov, prompter, workRoot, maxSteps)
}
