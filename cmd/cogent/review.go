package main

import (
	"context"
	"fmt"
	"os"

	"github.com/alaindong/cogent/internal/agent"
	"github.com/alaindong/cogent/internal/engine"
	"github.com/alaindong/cogent/internal/loop"
	"github.com/alaindong/cogent/internal/memory"
	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/sandbox"
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
type pipelineAdapter struct {
	mr *agent.MakerReviewer
}

// Iterate 见 loop.Pipeline 接口说明：转调 agent 流水线并把结果投影为 loop 视图。
func (a pipelineAdapter) Iterate(ctx context.Context, task string) (loop.PipelineResult, error) {
	r, err := a.mr.Iterate(ctx, task)
	if err != nil {
		return loop.PipelineResult{}, err
	}
	return loop.PipelineResult{
		Summary:  r.MakerSummary,
		Approved: r.Verdict.Approved,
		Feedback: r.Verdict.Feedback,
	}, nil
}

// buildPipeline 装配 maker/reviewer 双角色流水线：maker（可写池 + Auto）+ reviewer（只读池 + Ask）
// + git 回滚。maker 与 reviewer 可指不同模型（COGENT_REVIEWER_MODEL 覆盖），成本花在质量闸门刀刃上。
func buildPipeline(prov observe.Provider, prompter permission.Prompter, workRoot string) (loop.Pipeline, error) {
	llmc, err := newLLMClient()
	if err != nil {
		return nil, err
	}
	model := os.Getenv("COGENT_MODEL")
	makerDeps := engine.Deps{
		LLM:      llmc,
		Tools:    buildMakerPool(workRoot, prompter, prov.Tracer()),
		Memory:   memory.New(),
		Observe:  prov,
		Mode:     engine.ModeAuto,
		Model:    model,
		WorkRoot: workRoot,
	}
	reviewerDeps := engine.Deps{
		LLM:      llmc,
		Tools:    buildReviewerPool(workRoot),
		Observe:  prov,
		Mode:     engine.ModeAsk,
		Model:    reviewerModel(model),
		WorkRoot: workRoot,
	}
	sb := sandbox.New(sandbox.Config{WorkRoot: workRoot, Enabled: false})
	mr := agent.NewMakerReviewer(makerDeps, reviewerDeps, gitDiscarder{sb: sb})
	return pipelineAdapter{mr: mr}, nil
}

// reviewerModel 返回审查者使用的模型：优先 COGENT_REVIEWER_MODEL，否则沿用 maker 模型。
func reviewerModel(makerModel string) string {
	if m := os.Getenv("COGENT_REVIEWER_MODEL"); m != "" {
		return m
	}
	return makerModel
}

// buildOrchestrator 装配目标循环编排器：review=true 走双角色流水线，否则走单 engine 执行体。
// 返回的 cleanup 用于释放可能持有的 MCP 连接（review 模式不连接 MCP，返回空清理）。
func buildOrchestrator(
	ctx context.Context,
	prov observe.Provider,
	prompter permission.Prompter,
	mode engine.Mode,
	sessionID, workRoot string,
	review bool,
) (loop.Orchestrator, func(), error) {
	noop := func() {}
	if review {
		pipeline, err := buildPipeline(prov, prompter, workRoot)
		if err != nil {
			return nil, noop, err
		}
		orch, err := loop.New(loop.Deps{Pipeline: pipeline, Tracer: prov.Tracer(), Meter: prov.Meter()})
		if err != nil {
			return nil, noop, fmt.Errorf("init loop: %w", err)
		}
		return orch, noop, nil
	}
	mgr, err := buildMCPManager(ctx, workRoot, prov.Tracer())
	if err != nil {
		return nil, noop, err
	}
	eng, err := buildEngine(prov, prompter, mode, sessionID, workRoot, mgr.Tools())
	if err != nil {
		_ = mgr.Close()
		return nil, noop, err
	}
	orch, err := loop.New(loop.Deps{Engine: eng, Tracer: prov.Tracer(), Meter: prov.Meter()})
	if err != nil {
		_ = mgr.Close()
		return nil, noop, fmt.Errorf("init loop: %w", err)
	}
	return orch, func() { _ = mgr.Close() }, nil
}
