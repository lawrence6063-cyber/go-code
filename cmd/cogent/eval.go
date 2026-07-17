package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/alaindong/cogent/internal/engine"
	"github.com/alaindong/cogent/internal/eval"
	"github.com/alaindong/cogent/internal/eval/adapter"
	"github.com/alaindong/cogent/internal/eval/adapter/native"
	"github.com/alaindong/cogent/internal/eval/adapter/polyglot"
	"github.com/alaindong/cogent/internal/eval/adapter/swebench"
	"github.com/alaindong/cogent/internal/eval/adapter/terminalbench"
	"github.com/alaindong/cogent/internal/eval/scaffold"
	"github.com/alaindong/cogent/internal/loop"
	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/session"
	"github.com/alaindong/cogent/internal/tui"
)

// evalRunOptions 聚合 eval run 子命令的运行选项。
type evalRunOptions struct {
	dataset          string        // 评测套件：native（默认）| polyglot | swebench | terminalbench
	tasksDir         string        // native 评测集目录（默认 eval/tasks）
	polyglotDir      string        // polyglot 数据集根目录（--dataset=polyglot 时必填）
	swebenchFile     string        // swebench 数据集 JSONL 文件（--dataset=swebench 时必填）
	swebenchRepos    string        // swebench 本地仓库镜像根目录（--dataset=swebench 时必填）
	terminalbenchDir string        // terminal-bench 数据集根目录（--dataset=terminalbench 时必填）
	exercises        []string      // polyglot 只跑指定练习 slug（可空）
	limit            int           // polyglot/swebench/terminalbench 取样上限（0=不限）
	filter           native.Filter // 标签筛选（id / 维度 / 难度 / 语言）
	budget           loop.Budget   // 全局预算覆盖（零值不覆盖）
	artifactDir      string        // 归档根目录
	out              string        // 报告输出路径（.md；同时写同名 .json）
	model            string        // 覆盖模型（省成本用便宜模型）
	maxSteps         int           // 单轮 ReAct 最大轮数
	concurrency      int           // 并发样本数
	dryRun           bool          // 只加载并打印 case 列表，不跑
}

// newEvalCmd 构造 eval 子命令：Headless 批量跑分运行器（EVAL_SPEC §6）。
func newEvalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "评测集批量跑分：跑一批任务 → 聚合指标 → 出 Markdown/JSON 基线报告",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newEvalRunCmd())
	cmd.AddCommand(newEvalCompareCmd())
	cmd.AddCommand(newEvalScaffoldSelectCmd())
	return cmd
}

// newEvalRunCmd 构造 eval run 子命令及其 flag（对齐 EVAL_SPEC §6.8）。
func newEvalRunCmd() *cobra.Command {
	var (
		dataset                     string
		tasksDir, polyglotDir       string
		swebenchFile, swebenchRepos string
		terminalbenchDir            string
		exercises                   string
		limit                       int
		id, caps, diffs, langs      string
		maxIter                     int
		maxCost                     float64
		maxWall                     time.Duration
		artifactDir, out, model     string
		maxSteps, concurrency       int
		dryRun                      bool
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "跑评测集（native / polyglot / swebench / terminalbench，按标签筛选），产出基线报告",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(artifactDir) == "" {
				artifactDir = filepath.Join("eval-artifacts", time.Now().UTC().Format("20060102-150405"))
			}
			return runEvalCmd(cmd.Context(), evalRunOptions{
				dataset:          dataset,
				tasksDir:         tasksDir,
				polyglotDir:      polyglotDir,
				swebenchFile:     swebenchFile,
				swebenchRepos:    swebenchRepos,
				terminalbenchDir: terminalbenchDir,
				exercises:        splitCSV(exercises),
				limit:            limit,
				filter: native.Filter{
					IDs:          splitCSV(id),
					Capabilities: splitCSV(caps),
					Difficulties: splitCSV(diffs),
					Languages:    splitCSV(langs),
				},
				budget:      loop.Budget{MaxIterations: maxIter, MaxCostUSD: maxCost, MaxWallClock: maxWall},
				artifactDir: artifactDir,
				out:         out,
				model:       model,
				maxSteps:    maxSteps,
				concurrency: concurrency,
				dryRun:      dryRun,
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&dataset, "dataset", "native", "评测套件：native | polyglot | swebench | terminalbench")
	f.StringVar(&tasksDir, "tasks-dir", filepath.Join("eval", "tasks"), "native 评测集目录")
	f.StringVar(&polyglotDir, "polyglot-dir", "", "polyglot 数据集根目录（--dataset=polyglot 时必填）")
	f.StringVar(&swebenchFile, "swebench-file", "", "swebench 数据集 JSONL 文件（--dataset=swebench 时必填）")
	f.StringVar(&swebenchRepos, "swebench-repos", "", "swebench 本地仓库镜像根目录（--dataset=swebench 时必填）")
	f.StringVar(&terminalbenchDir, "terminalbench-dir", "", "terminal-bench 数据集根目录（--dataset=terminalbench 时必填）")
	f.StringVar(&exercises, "exercise", "", "polyglot 只跑指定练习 slug（可逗号分隔）")
	f.IntVar(&limit, "limit", 0, "取样上限（polyglot 为每语言上限，swebench/terminalbench 为总数上限，0 = 不限）")
	f.StringVar(&id, "id", "", "只跑指定任务 id（可逗号分隔，native）")
	f.StringVar(&caps, "capability", "", "按维度标签筛选（可逗号分隔）")
	f.StringVar(&diffs, "difficulty", "", "按难度筛选（easy|medium|hard，可逗号分隔）")
	f.StringVar(&langs, "language", "", "按语言筛选（可逗号分隔）")
	f.IntVar(&maxIter, "max-iterations", 0, "全局外层循环最大轮数覆盖（0 = 用 case 自带/默认）")
	f.Float64Var(&maxCost, "max-cost", 0, "全局累计成本上限覆盖（美元，0 = 不覆盖）")
	f.DurationVar(&maxWall, "max-wallclock", 0, "全局墙钟上限覆盖（0 = 不覆盖）")
	f.StringVar(&artifactDir, "artifact-dir", "", "归档根目录（默认 ./eval-artifacts/<ts>）")
	f.StringVar(&out, "out", "", "报告输出路径（.md；同时写同名 .json；默认写入 artifact-dir/report.md）")
	f.StringVar(&model, "model", "", "覆盖模型（省成本用便宜模型）")
	f.IntVar(&maxSteps, "max-steps", 0, "单轮 ReAct 最大轮数（0 = 走 env/默认）")
	f.IntVar(&concurrency, "n-concurrent", 1, "并发样本数")
	f.BoolVar(&dryRun, "dry-run", false, "只加载并打印 case 列表，不跑")
	return cmd
}

// runEvalCmd 加载评测集、（可选）建 Executor 跑一批、聚合并落地报告。
func runEvalCmd(ctx context.Context, opts evalRunOptions) error {
	if opts.model != "" {
		_ = os.Setenv("COGENT_MODEL", opts.model)
	}
	if opts.dryRun {
		return evalDryRun(opts)
	}
	// eval 跑在一次性隔离工作区副本上：放宽 agent 沙箱以继承宿主工具链（否则 go test/pytest 等
	// 在受限 env 下跑不通、agent 陷入修环境泥潭直到超时）。用户显式设置则尊重其选择。
	if os.Getenv("COGENT_SANDBOX_ENABLED") == "" {
		_ = os.Setenv("COGENT_SANDBOX_ENABLED", "false")
	}
	adp, suite, err := buildEvalAdapter(opts)
	if err != nil {
		return err
	}
	if err := adp.Prepare(ctx); err != nil {
		return fmt.Errorf("prepare %s suite: %w", suite, err)
	}
	cases, err := adp.Cases(ctx)
	if err != nil {
		return fmt.Errorf("load cases: %w", err)
	}
	if len(cases) == 0 {
		return errors.New("no cases matched the given filters")
	}
	fmt.Printf("cogent eval — suite=%s running %d case(s), artifact-dir=%s\n", suite, len(cases), opts.artifactDir)
	report, err := eval.NewRunner().Run(ctx, cases, eval.RunOptions{
		Executor:    evalExecutor{mode: engine.ModeAuto, maxSteps: opts.maxSteps},
		Suite:       suite,
		Concurrency: opts.concurrency,
		Budget:      opts.budget,
		ArtifactDir: opts.artifactDir,
	})
	report.Model = os.Getenv("COGENT_MODEL")
	if err != nil {
		return fmt.Errorf("run suite: %w", err)
	}
	if suite == "swebench" {
		exportSwebenchPredictions(ctx, opts, report.Model)
	}
	return writeReport(report, opts.artifactDir, opts.out)
}

// exportSwebenchPredictions 从各样本工作区抽取 agent 产出的补丁，导出官方 predictions.jsonl
// （接入模式 A，EVAL_SPEC §5.2.1）：交 sb-cli 云端 / run_evaluation 本地 Docker 判定。尽力而为——
// 单条抽取失败仅告警不中断（本地模式 B 报告已产出，predictions 是给官方判定的额外产物）。
func exportSwebenchPredictions(ctx context.Context, opts evalRunOptions, model string) {
	insts, err := swebench.LoadInstances(opts.swebenchFile, swebench.Filter{InstanceIDs: opts.filter.IDs, Limit: opts.limit})
	if err != nil {
		fmt.Fprintf(os.Stderr, "swebench predictions: load instances failed: %v\n", err)
		return
	}
	ws := evalWorkspaceDir(opts.artifactDir)
	preds := make([]swebench.Prediction, 0, len(insts))
	for _, inst := range insts {
		workRoot := filepath.Join(ws, sanitizeCaseID(inst.InstanceID), "repo")
		p, err := swebench.CollectPrediction(ctx, inst, workRoot, model)
		if err != nil {
			fmt.Fprintf(os.Stderr, "swebench predictions: collect %s failed: %v\n", inst.InstanceID, err)
			continue
		}
		preds = append(preds, p)
	}
	out := filepath.Join(opts.artifactDir, "predictions.jsonl")
	if err := writeToFile(out, func(w io.Writer) error { return swebench.WritePredictions(w, preds) }); err != nil {
		fmt.Fprintf(os.Stderr, "swebench predictions: write failed: %v\n", err)
		return
	}
	fmt.Printf("swebench predictions: %d patch(es) → %s (feed to sb-cli / run_evaluation)\n", len(preds), out)
}

// sanitizeCaseID 把 instance_id 里的路径分隔符换成下划线，与 swebench Adapter 的工作区命名一致。
func sanitizeCaseID(id string) string {
	return strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(id)
}

// buildEvalAdapter 按 --dataset 选择并装配 Adapter，返回 (adapter, suite 名)。
func buildEvalAdapter(opts evalRunOptions) (adapter.Adapter, string, error) {
	ws := evalWorkspaceDir(opts.artifactDir)
	switch strings.ToLower(strings.TrimSpace(opts.dataset)) {
	case "", "native":
		return native.Adapter{TasksDir: opts.tasksDir, WorkspaceDir: ws, Filter: opts.filter}, "native", nil
	case "polyglot":
		if strings.TrimSpace(opts.polyglotDir) == "" {
			return nil, "", errors.New("--polyglot-dir is required for --dataset=polyglot")
		}
		return polyglot.Adapter{
			Root:         opts.polyglotDir,
			WorkspaceDir: ws,
			Filter: polyglot.Filter{
				Languages: opts.filter.Languages,
				Exercises: opts.exercises,
				Limit:     opts.limit,
			},
		}, "polyglot", nil
	case "swebench":
		if strings.TrimSpace(opts.swebenchFile) == "" || strings.TrimSpace(opts.swebenchRepos) == "" {
			return nil, "", errors.New("--swebench-file and --swebench-repos are required for --dataset=swebench")
		}
		return swebench.Adapter{
			DatasetFile:  opts.swebenchFile,
			ReposDir:     opts.swebenchRepos,
			WorkspaceDir: ws,
			Filter:       swebench.Filter{InstanceIDs: opts.filter.IDs, Limit: opts.limit},
		}, "swebench", nil
	case "terminalbench":
		if strings.TrimSpace(opts.terminalbenchDir) == "" {
			return nil, "", errors.New("--terminalbench-dir is required for --dataset=terminalbench")
		}
		return terminalbench.Adapter{
			DatasetDir:   opts.terminalbenchDir,
			WorkspaceDir: ws,
			Filter: terminalbench.Filter{
				IDs:          opts.filter.IDs,
				Tags:         opts.filter.Capabilities,
				Difficulties: opts.filter.Difficulties,
				Limit:        opts.limit,
			},
		}, "terminalbench", nil
	default:
		return nil, "", fmt.Errorf("unknown --dataset %q (want native | polyglot | swebench | terminalbench)", opts.dataset)
	}
}

// evalWorkspaceDir 返回工作区副本根目录，置于 os.TempDir() 下（脱离 cogent git 仓库）：
// 避免 agent 的 GitSnapshotter（git add -A）作用到父仓库、go 工具链下载污染仓库树。
func evalWorkspaceDir(artifactDir string) string {
	return filepath.Join(os.TempDir(), "cogent-eval", filepath.Base(artifactDir), "workspaces")
}

// evalDryRun 只加载并打印匹配的任务列表（校验 Adapter / 筛选），不建副本、不跑 agent。
func evalDryRun(opts evalRunOptions) error {
	switch strings.ToLower(strings.TrimSpace(opts.dataset)) {
	case "polyglot":
		return evalDryRunPolyglot(opts)
	case "swebench":
		return evalDryRunSwebench(opts)
	case "terminalbench":
		return evalDryRunTerminalbench(opts)
	default:
		return evalDryRunNative(opts)
	}
}

// evalDryRunNative 打印命中筛选的 native 任务列表。
func evalDryRunNative(opts evalRunOptions) error {
	specs, err := native.Load(opts.tasksDir, opts.filter)
	if err != nil {
		return fmt.Errorf("load specs: %w", err)
	}
	fmt.Printf("cogent eval --dry-run (native) — %d task(s) matched:\n", len(specs))
	for _, s := range specs {
		fmt.Printf("  - %-24s difficulty=%-6s caps=%v langs=%v workdir=%s\n",
			s.YAML.ID, s.YAML.Difficulty, s.YAML.Capabilities, s.YAML.Languages, s.YAML.Workdir)
	}
	return nil
}

// evalDryRunPolyglot 打印命中筛选的 polyglot 练习列表（校验数据集路径与筛选）。
func evalDryRunPolyglot(opts evalRunOptions) error {
	if strings.TrimSpace(opts.polyglotDir) == "" {
		return errors.New("--polyglot-dir is required for --dataset=polyglot")
	}
	specs, err := polyglot.Load(opts.polyglotDir, polyglot.Filter{
		Languages: opts.filter.Languages,
		Exercises: opts.exercises,
		Limit:     opts.limit,
	})
	if err != nil {
		return fmt.Errorf("load polyglot specs: %w", err)
	}
	fmt.Printf("cogent eval --dry-run (polyglot) — %d exercise(s) matched:\n", len(specs))
	for _, s := range specs {
		fmt.Printf("  - %s/%s\n", s.Language, s.Slug)
	}
	return nil
}

// evalDryRunSwebench 打印命中筛选的 swebench 样本列表（校验数据集文件与筛选）。
func evalDryRunSwebench(opts evalRunOptions) error {
	if strings.TrimSpace(opts.swebenchFile) == "" {
		return errors.New("--swebench-file is required for --dataset=swebench")
	}
	insts, err := swebench.LoadInstances(opts.swebenchFile, swebench.Filter{InstanceIDs: opts.filter.IDs, Limit: opts.limit})
	if err != nil {
		return fmt.Errorf("load swebench instances: %w", err)
	}
	fmt.Printf("cogent eval --dry-run (swebench) — %d instance(s) matched:\n", len(insts))
	for _, inst := range insts {
		fmt.Printf("  - %-40s repo=%s base=%.10s\n", inst.InstanceID, inst.Repo, inst.BaseCommit)
	}
	return nil
}

// evalDryRunTerminalbench 打印命中筛选的 terminal-bench 任务列表（校验数据集路径与筛选）。
func evalDryRunTerminalbench(opts evalRunOptions) error {
	if strings.TrimSpace(opts.terminalbenchDir) == "" {
		return errors.New("--terminalbench-dir is required for --dataset=terminalbench")
	}
	specs, err := terminalbench.Load(opts.terminalbenchDir, terminalbench.Filter{
		IDs:          opts.filter.IDs,
		Tags:         opts.filter.Capabilities,
		Difficulties: opts.filter.Difficulties,
		Limit:        opts.limit,
	})
	if err != nil {
		return fmt.Errorf("load terminalbench tasks: %w", err)
	}
	fmt.Printf("cogent eval --dry-run (terminalbench) — %d task(s) matched:\n", len(specs))
	for _, s := range specs {
		fmt.Printf("  - %-30s difficulty=%-6s tags=%v\n", s.ID, s.YAML.Difficulty, s.YAML.Tags)
	}
	return nil
}

// writeReport 把报告写为 <out>.md 与 <out>.json（out 为空时落到 artifactDir/report.md）。
func writeReport(report eval.Report, artifactDir, out string) error {
	if strings.TrimSpace(out) == "" {
		out = filepath.Join(artifactDir, "report.md")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return fmt.Errorf("mkdir report dir: %w", err)
	}
	jsonPath := strings.TrimSuffix(out, filepath.Ext(out)) + ".json"
	if err := writeToFile(out, report.WriteMarkdown); err != nil {
		return err
	}
	if err := writeToFile(jsonPath, report.WriteJSON); err != nil {
		return err
	}
	fmt.Printf("report: %s\n        %s\n", out, jsonPath)
	fmt.Printf("summary: %d/%d passed (%.1f%%)\n",
		report.Metrics.Passed, report.Metrics.Total, report.Metrics.SuccessRate*100)
	return nil
}

// writeToFile 用 write 回调把内容写入 path（统一 create/close/错误包装）。
func writeToFile(path string, write func(w io.Writer) error) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := write(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	return f.Close()
}

// splitCSV 把逗号分隔字符串切成非空 trim 后的切片；空串返回 nil。
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// evalCompareExitRegress 是 compare --fail-on-regress 检出退化时的退出码（EVAL_SPEC §6.8）。
const evalCompareExitRegress = 3

// newEvalCompareCmd 构造 eval compare 子命令：对比两份 report.json，输出指标 delta 与退化清单。
func newEvalCompareCmd() *cobra.Command {
	var base, head, out string
	var failOnRegress bool
	cmd := &cobra.Command{
		Use:   "compare",
		Short: "回归对比：对比基线与当前 report.json，标出退化项（--fail-on-regress 退化时退出码 3）",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runEvalCompare(base, head, out, failOnRegress)
		},
	}
	f := cmd.Flags()
	f.StringVar(&base, "base", "", "基线报告 report.json 路径（必填）")
	f.StringVar(&head, "head", "", "当前报告 report.json 路径（必填）")
	f.StringVar(&out, "out", "", "delta 报告输出路径（默认打印到 stdout）")
	f.BoolVar(&failOnRegress, "fail-on-regress", false, "检出退化（case 由通过变失败）时以退出码 3 结束")
	return cmd
}

// runEvalCompare 加载两份报告、对比、写 delta，并按 --fail-on-regress 决定退出码。
func runEvalCompare(base, head, out string, failOnRegress bool) error {
	if strings.TrimSpace(base) == "" || strings.TrimSpace(head) == "" {
		return errors.New("both --base and --head are required")
	}
	baseReport, err := loadReportFile(base)
	if err != nil {
		return err
	}
	headReport, err := loadReportFile(head)
	if err != nil {
		return err
	}
	cmp := eval.Compare(baseReport, headReport)
	if err := emitComparison(cmp, out); err != nil {
		return err
	}
	if failOnRegress && cmp.HasRegression() {
		fmt.Fprintf(os.Stderr, "regression detected: %d case(s) went from pass to fail\n", len(cmp.Regressed))
		os.Exit(evalCompareExitRegress)
	}
	return nil
}

// loadReportFile 从路径读取并反序列化一份 report.json。
func loadReportFile(path string) (eval.Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return eval.Report{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return eval.LoadReport(f)
}

// emitComparison 把对比结论写到 out（为空则打印到 stdout）。
func emitComparison(cmp eval.Comparison, out string) error {
	if strings.TrimSpace(out) == "" {
		return cmp.WriteMarkdown(os.Stdout)
	}
	return writeToFile(out, cmp.WriteMarkdown)
}

// scaffoldSelectResult 是单实例选择的过程记录（S-M 过程指标：候选数/选中理由/补丁聚焦度）。
type scaffoldSelectResult struct {
	InstanceID    string `json:"instance_id"`
	NumCandidates int    `json:"num_candidates"`
	Selected      bool   `json:"selected"`    // 是否选出非空补丁
	Reason        string `json:"reason"`      // 选择理由（Select 返回）
	PatchBytes    int    `json:"patch_bytes"` // 选中补丁字节数
}

// scaffoldSelectReport 汇总 scaffold-select 的过程指标，落 <artifact-dir>/scaffold-select-report.json。
type scaffoldSelectReport struct {
	Model         string                 `json:"model"`
	TotalInstance int                    `json:"total_instances"`
	SelectedCount int                    `json:"selected_instances"`
	Results       []scaffoldSelectResult `json:"results"`
}

// newEvalScaffoldSelectCmd 构造 eval scaffold-select 子命令（SCAFFOLD_SPEC §4.1 / S-D）：
// 从 best-of-N 产物目录读取每实例候选（+可选 Docker 信号），跑纯 Go Selector 选出 final patch，
// 导出官方 predictions.jsonl，并落一份过程指标报告。选择逻辑不触网络/Docker（守 §3.3）。
func newEvalScaffoldSelectCmd() *cobra.Command {
	var artifactDir, out, model string
	cmd := &cobra.Command{
		Use:   "scaffold-select",
		Short: "从 best-of-N 候选 + 可执行信号中选出每实例 final patch，导出 predictions.jsonl（SCAFFOLD_SPEC S-D）",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runScaffoldSelect(artifactDir, out, model)
		},
	}
	f := cmd.Flags()
	f.StringVar(&artifactDir, "artifact-dir", "", "scaffold 产物根目录（含 candidates/ 与可选 signals/，必填）")
	f.StringVar(&out, "out", "", "predictions.jsonl 输出路径（默认 <artifact-dir>/predictions.jsonl）")
	f.StringVar(&model, "model", "", "写入 predictions 的 model_name_or_path（默认取 COGENT_MODEL 或 cogent-scaffold）")
	return cmd
}

// runScaffoldSelect 执行选择：加载产物 → 逐实例 Select → 写 predictions.jsonl + 过程报告。
func runScaffoldSelect(artifactDir, out, model string) error {
	if strings.TrimSpace(artifactDir) == "" {
		return errors.New("--artifact-dir is required")
	}
	if strings.TrimSpace(model) == "" {
		if model = os.Getenv("COGENT_MODEL"); strings.TrimSpace(model) == "" {
			model = "cogent-scaffold"
		}
	}
	insts, err := scaffold.LoadArtifacts(artifactDir)
	if err != nil {
		return fmt.Errorf("load scaffold artifacts: %w", err)
	}
	if len(insts) == 0 {
		return fmt.Errorf("no candidates found under %s/candidates", artifactDir)
	}
	preds := make([]swebench.Prediction, 0, len(insts))
	report := scaffoldSelectReport{Model: model, TotalInstance: len(insts)}
	for _, ic := range insts {
		patch, reason := scaffold.Select(ic.Candidates)
		report.Results = append(report.Results, scaffoldSelectResult{
			InstanceID:    ic.InstanceID,
			NumCandidates: len(ic.Candidates),
			Selected:      strings.TrimSpace(patch) != "",
			Reason:        reason,
			PatchBytes:    len(patch),
		})
		if strings.TrimSpace(patch) == "" {
			fmt.Fprintf(os.Stderr, "scaffold-select: %s produced no patch (%s)\n", ic.InstanceID, reason)
			continue
		}
		report.SelectedCount++
		preds = append(preds, swebench.Prediction{
			InstanceID:      ic.InstanceID,
			ModelNameOrPath: model,
			ModelPatch:      patch,
		})
	}
	if strings.TrimSpace(out) == "" {
		out = filepath.Join(artifactDir, "predictions.jsonl")
	}
	if err := writeToFile(out, func(w io.Writer) error { return swebench.WritePredictions(w, preds) }); err != nil {
		return fmt.Errorf("write predictions: %w", err)
	}
	reportPath := filepath.Join(artifactDir, "scaffold-select-report.json")
	if err := writeToFile(reportPath, func(w io.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}); err != nil {
		return fmt.Errorf("write scaffold report: %w", err)
	}
	fmt.Printf("scaffold-select: %d/%d instance(s) selected → %s\n", report.SelectedCount, report.TotalInstance, out)
	fmt.Printf("               process report → %s\n", reportPath)
	return nil
}

// evalExecutor 复用 buildOrchestrator/buildVerifier 把一条 Case 跑成 LoopResult（EVAL_SPEC §6.2）。
// 每条 case 用独立 observe provider + 自动批准 prompter + 新会话，并按 case 工作区副本重建编排器。
type evalExecutor struct {
	mode     engine.Mode // 运行档位（默认 ModeAuto）
	maxSteps int         // 单轮 ReAct 最大轮数
}

// Run 见 eval.Executor 接口说明：装配编排器、跑目标循环、drain 事件流到 LoopFinished。
func (e evalExecutor) Run(ctx context.Context, c adapter.Case, art string) (loop.LoopResult, error) {
	prov, err := observe.New(evalObserveConfig(art))
	if err != nil {
		return loop.LoopResult{}, fmt.Errorf("init observe: %w", err)
	}
	defer func() { _ = prov.Shutdown(context.Background()) }()

	review := hasCapability(c.Meta, "review")
	orch, cleanup, err := buildOrchestrator(
		ctx, prov, tui.NewYesPrompter(), e.mode,
		session.NewSessionID(), c.Goal.WorkRoot, review, false, e.maxSteps)
	if err != nil {
		return loop.LoopResult{}, err
	}
	defer cleanup()

	events, err := orch.RunGoal(ctx, c.Goal)
	if err != nil {
		return loop.LoopResult{}, fmt.Errorf("run goal: %w", err)
	}
	return drainToResult(ctx, events)
}

// evalObserveConfig 按环境构造 observe 配置；启用 trace 时把该 case 的 span 落到 art/traces。
func evalObserveConfig(art string) observe.Config {
	cfg := observeConfig()
	if cfg.Enabled && strings.TrimSpace(art) != "" {
		cfg.TraceDir = filepath.Join(art, "traces")
	}
	return cfg
}

// drainToResult 消费只读事件流直到通道关闭，返回最后一次 LoopFinished 携带的 LoopResult。
// 若通道关闭前未见 LoopFinished：ctx 已取消（超时/上游取消）时视为 canceled 结局（终局事件在
// 取消竞态下可能被丢弃，不应误判为 fatal）；否则才按异常 fail-closed 报错。
func drainToResult(ctx context.Context, events <-chan loop.LoopEvent) (loop.LoopResult, error) {
	var result *loop.LoopResult
	for ev := range events {
		if ev.Type == loop.LoopFinished && ev.Result != nil {
			r := *ev.Result
			result = &r
		}
	}
	if result != nil {
		return *result, nil
	}
	if ctx.Err() != nil {
		return loop.LoopResult{Outcome: loop.OutcomeCanceled}, nil
	}
	return loop.LoopResult{}, errors.New("event stream closed without LoopFinished")
}

// hasCapability 报告 meta 的维度标签是否含 want（如 review-capability 任务自动启用双角色）。
func hasCapability(meta adapter.Meta, want string) bool {
	for _, c := range meta.Capabilities {
		if strings.EqualFold(c, want) {
			return true
		}
	}
	return false
}
