// Package swebench 是 EVAL_SPEC §5.2 的 swebench.Adapter：把 SWE-bench(_Lite) 数据集的真实仓库
// 修复任务映射为 cogent 的 adapter.Case，是继 native / polyglot 之后第三个 Adapter，用于覆盖
// 「跨仓库 / 架构级困难任务 + 探索能力」维度（EVAL_SPEC §4.5 hard 档、§5.2.3 个人项目最小可行路径第 2 步）。
//
// 两种接入模式（EVAL_SPEC §5.2.1）本 Adapter 均支持：
//   - 模式 A（官方判定，推荐）：agent 产出补丁 → WritePredictions 导出 predictions.jsonl →
//     官方 sb-cli 云端 / run_evaluation 本地 Docker 判定（免本仓复刻各仓库测试环境）。
//   - 模式 B（Adapter 接回）：instanceVerifier 在本地瞬态套隐藏 test_patch、跑 FAIL_TO_PASS/PASS_TO_PASS
//     判定，吃到 loop 过程指标（Pass@k / 收敛轮数）。模式 B 需要各仓库测试环境可用（真实 SWE-bench
//     多需 Docker），本仓的 fixture oracle 测试用自包含 git 仓库在无 Docker 下验证该判定路径。
//
// 关键不变量（对齐 native / polyglot）：
//   - 数据集与仓库均由用户预先准备（JSONL 文件 + 本地仓库镜像），Adapter 只读取、不联网拉取；
//   - 工作区隔离——每条样本 clone 到独立副本再跑，绝不污染用户的仓库镜像；
//   - gold patch 绝不喂 agent；隐藏 test_patch 判定时瞬态应用、判完还原（EVAL_SPEC §5.2.1）。
package swebench

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alaindong/cogent/internal/eval/adapter"
	"github.com/alaindong/cogent/internal/loop"
	"github.com/alaindong/cogent/internal/verify"
)

// defaultVerifyTimeout 是判定命令单次执行的超时上限（真实仓库测试偏慢，给足余量）。
const defaultVerifyTimeout = 20 * time.Minute

// Adapter 把 SWE-bench 数据集映射为 adapter.Case（实现 adapter.Adapter）。
type Adapter struct {
	DatasetFile   string        // SWE-bench 数据集 JSONL 文件（用户从 HuggingFace 导出）
	ReposDir      string        // 本地仓库镜像根目录（<repos>/<org>__<name> 或 <repos>/<org>/<name>）
	WorkspaceDir  string        // 每条样本工作区副本的根
	Filter        Filter        // instance_id / repo / 数量筛选
	VerifyTimeout time.Duration // 判定命令超时；<=0 用默认
}

// Name 见 adapter.Adapter 接口说明。
func (a Adapter) Name() string { return "swebench" }

// Prepare 见 adapter.Adapter 接口说明：校验数据集文件存在、仓库镜像根存在、git 可用（fail-fast）。
func (a Adapter) Prepare(_ context.Context) error {
	if !fileExists(a.DatasetFile) {
		return fmt.Errorf("swebench dataset file not found: %s (export SWE-bench_Lite to JSONL first)", a.DatasetFile)
	}
	if strings.TrimSpace(a.ReposDir) == "" || !dirExists(a.ReposDir) {
		return fmt.Errorf("swebench repos mirror dir not found: %s (clone target repos locally first)", a.ReposDir)
	}
	if !gitAvailable() {
		return errors.New("git not found on PATH")
	}
	return nil
}

// Cases 见 adapter.Adapter 接口说明：加载样本、为每条样本建隔离工作区副本并组 Case。
// ctx 取消即停止产出。写 test_patch 到工作区外供瞬态判定使用（agent 够不到、不可篡改）。
func (a Adapter) Cases(ctx context.Context) ([]adapter.Case, error) {
	insts, err := LoadInstances(a.DatasetFile, a.Filter)
	if err != nil {
		return nil, err
	}
	cases := make([]adapter.Case, 0, len(insts))
	for _, inst := range insts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c, err := a.buildCase(ctx, inst)
		if err != nil {
			return nil, err
		}
		cases = append(cases, c)
	}
	return cases, nil
}

// buildCase 为一条样本 clone 隔离工作区、写隐藏 test_patch 到工作区外，并组装 adapter.Case。
func (a Adapter) buildCase(ctx context.Context, inst Instance) (adapter.Case, error) {
	mirror, err := a.mirrorPath(inst.Repo)
	if err != nil {
		return adapter.Case{}, err
	}
	caseDir := filepath.Join(a.WorkspaceDir, sanitize(inst.InstanceID))
	workRoot := filepath.Join(caseDir, "repo")
	if err := os.RemoveAll(caseDir); err != nil {
		return adapter.Case{}, fmt.Errorf("clean case dir %s: %w", caseDir, err)
	}
	if err := cloneAt(ctx, mirror, workRoot, inst.BaseCommit); err != nil {
		return adapter.Case{}, fmt.Errorf("prepare workspace for %s: %w", inst.InstanceID, err)
	}
	patchFile := filepath.Join(caseDir, "test_patch.diff")
	if err := os.WriteFile(patchFile, []byte(inst.TestPatch), 0o644); err != nil {
		return adapter.Case{}, fmt.Errorf("write test_patch for %s: %w", inst.InstanceID, err)
	}
	// 结构化定位（S-E）：默认关闭；开启时把 top-k 相关文件线索拼到意图末尾（只读工作区源码，不碰隐藏测试）。
	goalIntent := a.intent(inst) + localizeHint(workRoot, inst)
	return adapter.Case{
		ID:              "swebench/" + inst.InstanceID,
		Goal:            loop.Goal{Intent: goalIntent, WorkRoot: workRoot, Verifier: a.verifier(inst, patchFile), Budget: loop.DefaultBudget()},
		Meta:            adapter.Meta{Difficulty: "hard", Capabilities: []string{"exploration", "convergence"}, Source: "swebench"},
		ExpectedOutcome: "achieved",
		Timeout:         a.timeout(),
	}, nil
}

// verifier 构造该样本的独立判定器（模式 B：瞬态套隐藏测试判定）。
func (a Adapter) verifier(inst Instance, patchFile string) verify.Verifier {
	return instanceVerifier{inst: inst, patchFile: patchFile, timeout: a.timeout()}
}

// scaffoldEnvVar 控制是否注入 SWE-bench 专用 scaffold 指引。默认启用；
// 设为 "0"/"false"/"off"/"no" 时回退到最小意图，便于对比裸 agent 与 scaffold 的命中率（A/B）。
const scaffoldEnvVar = "COGENT_SWEBENCH_SCAFFOLD"

// scaffoldGuidance 是针对真实 issue 修复常见失败模式的工作指引（定位优先、最小单文件改动、
// 禁改测试/配置/vendored、禁盲跑依赖自证、补丁卫生），显著约束 agent 的散弹式改动与空耗迭代。
const scaffoldGuidance = "You are fixing a real GitHub issue in this repository. Work in this order:\n" +
	"1. LOCATE FIRST — before editing anything, use grep/find_files/read_file to find the specific " +
	"source file(s) and function(s) responsible for the described behavior, and read them to understand " +
	"the root cause. Do not edit until you have located the root cause.\n" +
	"2. MINIMAL FIX — make the smallest change that correctly resolves the root cause. Fixes for this " +
	"kind of issue almost always touch a SINGLE source file; do not scatter edits across files or do " +
	"unrelated refactors.\n" +
	"3. DO NOT modify: test files; vendored/third-party code (paths with /vendor/, /packages/, " +
	"/_vendor/, site-packages); build/config files (setup.py, pyproject.toml, setup.cfg, tox.ini); or " +
	"changelog/docs (CHANGES*, *.rst, docs/). A hidden test suite you cannot see will judge the fix.\n" +
	"4. DO NOT try to install dependencies, import the package, or run the project's test suite to " +
	"self-verify — the environment has no dependencies installed and doing so only wastes iterations. " +
	"Verify instead by re-reading the affected code path and reasoning about the change against the issue.\n" +
	"5. PATCH HYGIENE — leave no debug prints, commented-out code, or unrelated formatting changes; keep " +
	"the final diff limited to the lines needed for the fix.\n"

// legacyGuidance 是关闭 scaffold 时的最小意图（保留原行为，供 A/B 基线对比）。
const legacyGuidance = "Modify only non-test source files to fix the issue. Do NOT add or modify test files; " +
	"a hidden test suite will judge the fix. Make the smallest change that correctly resolves the issue.\n"

// intent 构造喂给 agent 的自然语言意图：issue 文本 + 工作指引。
// 默认注入 SWE-bench 专用 scaffold（针对定位/最小改动/补丁卫生等失败模式做约束），
// 可用 COGENT_SWEBENCH_SCAFFOLD=0 关闭回退最小意图。刻意不透露 FAIL_TO_PASS / test_patch
// （隐藏判定测试），避免 agent 面向测试作弊。
func (a Adapter) intent(inst Instance) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Repository: %s (at commit %s)\n\n", inst.Repo, inst.BaseCommit)
	b.WriteString("Resolve the following issue by editing the repository source code:\n\n")
	b.WriteString(strings.TrimSpace(inst.ProblemStatement))
	b.WriteString("\n\n---\n")
	if scaffoldEnabled() {
		b.WriteString(scaffoldGuidance)
	} else {
		b.WriteString(legacyGuidance)
	}
	return b.String()
}

// scaffoldEnabled 报告是否启用 SWE-bench scaffold（默认启用，仅显式关闭值才回退）。
func scaffoldEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(scaffoldEnvVar))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

// mirrorPath 解析仓库本地镜像路径：优先 SWE-bench 命名 <repos>/<org>__<name>，回退 <repos>/<org>/<name>。
func (a Adapter) mirrorPath(repo string) (string, error) {
	if strings.TrimSpace(repo) == "" {
		return "", errors.New("instance has empty repo")
	}
	candidates := []string{
		filepath.Join(a.ReposDir, strings.ReplaceAll(repo, "/", "__")),
		filepath.Join(a.ReposDir, filepath.FromSlash(repo)),
	}
	for _, c := range candidates {
		if dirExists(c) {
			return c, nil
		}
	}
	return "", fmt.Errorf("repo mirror not found for %q under %s (tried %s)", repo, a.ReposDir, strings.Join(candidates, ", "))
}

// timeout 返回单样本墙钟硬上限（VerifyTimeout 未设时用默认）。
func (a Adapter) timeout() time.Duration {
	if a.VerifyTimeout > 0 {
		return a.VerifyTimeout
	}
	return defaultVerifyTimeout
}

// dirExists 报告 path 是否为已存在的目录。
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// sanitize 把 instance_id 里的路径分隔符换成下划线，作为安全的目录名。
func sanitize(id string) string {
	return strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(id)
}
