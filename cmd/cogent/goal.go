package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/alaindong/cogent/internal/engine"
	"github.com/alaindong/cogent/internal/loop"
	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/sandbox"
	"github.com/alaindong/cogent/internal/session"
	"github.com/alaindong/cogent/internal/types"
	"github.com/alaindong/cogent/internal/verify"
	"github.com/alaindong/cogent/internal/worktree"
)

// verifyTimeout 是验收脚本单次执行的超时上限（经 sandbox 传到 os/exec）。
const verifyTimeout = 5 * time.Minute

// goalOptions 聚合 goal 子命令的运行选项。
type goalOptions struct {
	intent       string      // 自然语言目标
	mode         engine.Mode // 运行档位
	verifyScript string      // 验收脚本路径；为空则无判定器（fail-closed，跑到撞预算）
	review       bool        // 是否启用 maker/reviewer 双角色执行体
	worktree     bool        // 是否用 git worktree 暂存落盘（通过才 Merge，物理隔离）
	allowDirty   bool        // 是否跳过 --worktree 的脏工作树前置校验（风险自负）
	budget       loop.Budget // 三重预算护栏
	maxSteps     int         // 单轮 ReAct 最大轮数（0 = 走 env/默认）
}

// newGoalCmd 构造 goal 子命令：目标驱动循环——给定可验证终止条件，
// 持续「执行一轮 → 独立判定 → 不达标带反馈续跑」直到达标或撞预算。
func newGoalCmd() *cobra.Command {
	var (
		mode         string
		verifyScript string
		review       bool
		useWorktree  bool
		allowDirty   bool
		maxIter      int
		maxCost      float64
		maxWall      time.Duration
		maxSteps     int
	)
	cmd := &cobra.Command{
		Use:   "goal <intent>",
		Short: "目标驱动循环：持续迭代直到验收脚本通过或撞预算护栏（达目标才停）",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := parseMode(mode)
			if err != nil {
				return err
			}
			return runGoalCmd(cmd.Context(), goalOptions{
				intent:       strings.Join(args, " "),
				mode:         m,
				verifyScript: verifyScript,
				review:       review,
				worktree:     useWorktree,
				allowDirty:   allowDirty,
				budget:       loop.Budget{MaxIterations: maxIter, MaxCostUSD: maxCost, MaxWallClock: maxWall},
				maxSteps:     maxSteps,
			})
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "auto", "run mode: auto | plan | ask")
	cmd.Flags().StringVar(&verifyScript, "verify", "", "验收脚本路径（退出码 0 = 目标达成）")
	cmd.Flags().BoolVar(&review, "review", false, "启用 maker/reviewer 双角色（实现者改、独立审查者审，通过才落盘）")
	cmd.Flags().BoolVar(&useWorktree, "worktree", false, "双角色落盘用 git worktree 暂存（通过才 Merge，物理隔离；隐含 --review）")
	cmd.Flags().BoolVar(&allowDirty, "allow-dirty", false, "跳过 --worktree 的脏工作树前置校验（风险自负，merge 落盘可能失败）")
	cmd.Flags().IntVar(&maxIter, "max-iterations", 0, "外层循环最大轮数（0 = 保守默认 8）")
	cmd.Flags().Float64Var(&maxCost, "max-cost", 0, "累计 LLM 成本上限（美元，0 = 不限；需成本计量接入）")
	cmd.Flags().DurationVar(&maxWall, "max-wallclock", 0, "端到端墙钟上限（如 15m，0 = 不限）")
	cmd.Flags().IntVar(&maxSteps, "max-steps", 0, "单轮 ReAct 最大轮数（0 = 走 COGENT_MAX_REACT_STEPS env 或默认 50）")
	return cmd
}

// runGoalCmd 装配可观测/执行体/目标循环编排器，执行目标循环并流式渲染外层事件。
func runGoalCmd(ctx context.Context, opts goalOptions) error {
	prov, err := observe.New(observeConfig())
	if err != nil {
		return fmt.Errorf("init observe: %w", err)
	}
	defer func() { _ = prov.Shutdown(context.Background()) }()

	in := newInputReader(os.Stdin)
	prompter := newPrompter(in)
	wd, _ := os.Getwd()
	sid := session.NewSessionID()

	if opts.worktree && !opts.allowDirty {
		if err := ensureCleanWorktree(ctx, wd); err != nil {
			return err
		}
	}

	orch, cleanup, err := buildOrchestrator(ctx, prov, prompter, opts.mode, sid, wd, opts.review, opts.worktree, opts.maxSteps)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx, end := prov.Tracer().Start(ctx, "cogent.session",
		observe.Attr{Key: "session.id", Value: sid},
		observe.Attr{Key: "mode", Value: opts.mode.String()},
	)
	var runErr error
	defer func() { end(runErr, observe.Attr{Key: "outcome", Value: sessionOutcome(runErr)}) }()

	printGoalBanner(sid, opts)
	events, err := orch.RunGoal(ctx, loop.Goal{
		Intent:   augmentWithSkills(ctx, wd, opts.intent),
		WorkRoot: wd,
		Verifier: buildVerifier(opts.verifyScript, wd),
		Budget:   opts.budget,
	})
	if err != nil {
		return fmt.Errorf("run goal: %w", err)
	}
	runErr = renderLoopEvents(ctx, events)
	if errors.Is(runErr, context.Canceled) {
		return nil
	}
	return runErr
}

// ensureCleanWorktree 在启用 --worktree 时前置校验主仓库工作树是否干净。
// 脏工作树（未提交改动或未跟踪文件）会使隔离循环的 git merge --no-ff 落盘失败，
// 故 fail-closed 拦截并给出可操作指引；确需带脏运行可加 --allow-dirty（风险自负）。
func ensureCleanWorktree(ctx context.Context, workRoot string) error {
	sb := sandbox.New(sandbox.Config{WorkRoot: workRoot, Enabled: false})
	err := worktree.EnsureClean(ctx, sb)
	if errors.Is(err, worktree.ErrDirtyWorktree) {
		return fmt.Errorf("%w\n"+
			"  --worktree 隔离循环要求主仓库工作树干净（否则 merge 落盘会失败）。\n"+
			"  请先提交或暂存改动（git stash -u 或 git commit -am wip），或在干净 clone 上运行；\n"+
			"  确需带脏运行可加 --allow-dirty（风险自负）", err)
	}
	return err
}

// buildVerifier 构造独立判定器：脚本为空时返回 nil（fail-closed，跑到撞预算）。
// 验收脚本是开发者提供的可信控制面，需继承宿主 PATH（如 go 工具链），故沙箱 Enabled=false——
// 仍保留危险命令拦截 + WorkRoot 约束 + 超时，但不施加受限环境。
// 用 NewSandbox 工厂按传入工作根构造沙箱：使验收脚本能跑在 maker 改动所在目录（如 worktree 根），
// 让客观判据看到真实改动（修复 reviewer 双闸门短路客观 verify 的问题）；空工作根回退主工作区。
func buildVerifier(script, workRoot string) verify.Verifier {
	if strings.TrimSpace(script) == "" {
		return nil
	}
	return verify.ScriptVerifier{
		Script: script,
		NewSandbox: func(root string) sandbox.Sandbox {
			if strings.TrimSpace(root) == "" {
				root = workRoot
			}
			return sandbox.New(sandbox.Config{WorkRoot: root, Enabled: false, Timeout: verifyTimeout})
		},
	}
}

// printGoalBanner 打印目标循环的启动横幅。
func printGoalBanner(sid string, opts goalOptions) {
	fmt.Printf("cogent goal — session %s\n", sid)
	fmt.Printf("  intent : %s\n", opts.intent)
	if strings.TrimSpace(opts.verifyScript) == "" {
		fmt.Println("  verify : (none — fail-closed, runs until budget)")
	} else {
		fmt.Printf("  verify : %s\n", opts.verifyScript)
	}
	if opts.review || opts.worktree {
		if opts.worktree {
			fmt.Println("  exec   : maker/reviewer + worktree 暂存（隔离改 → 审 → 通过才 Merge 落盘）")
		} else {
			fmt.Println("  exec   : maker/reviewer (双角色：实现者改 + 独立审查者审，通过才落盘)")
		}
	}
}

// renderLoopEvents 消费外层循环事件流并渲染到 stdout；ctx 取消时安全收尾。
func renderLoopEvents(ctx context.Context, events <-chan loop.LoopEvent) error {
	for {
		select {
		case <-ctx.Done():
			fmt.Println("\n[interrupted]")
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			if err := printLoopEvent(ev); err != nil {
				return err
			}
		}
	}
}

// printLoopEvent 渲染单个外层事件：透传内层文本/工具事件，单独呈现轮次、判定与终局。
// 内层错误不中断外层渲染（外层会带反馈续跑），仅打印到 stderr。
func printLoopEvent(ev loop.LoopEvent) error {
	switch ev.Type {
	case loop.LoopIterationStart:
		fmt.Printf("\n=== iteration %d ===\ncogent> ", ev.Iteration+1)
	case loop.LoopInner:
		return printInnerEvent(ev.Inner)
	case loop.LoopVerify:
		if ev.Report != nil {
			printVerifyReport(*ev.Report)
		}
	case loop.LoopFinished:
		if ev.Result != nil {
			printLoopResult(*ev.Result)
		}
	}
	return nil
}

// printInnerEvent 透传内层 engine 事件；错误事件降级为告警，不冒泡中断外层循环。
func printInnerEvent(inner *types.StreamEvent) error {
	if inner == nil {
		return nil
	}
	if inner.Type == types.EventError {
		if inner.Err != nil {
			fmt.Fprintln(os.Stderr, "\n[inner error]", inner.Err)
		}
		return nil
	}
	return printEvent(*inner)
}

// printVerifyReport 渲染一次独立判定的结论。
func printVerifyReport(r verify.Report) {
	status := "NOT PASSED"
	if r.Passed {
		status = "PASSED"
	}
	fmt.Printf("\n[verify] %s — %s\n", status, r.Summary)
}

// printLoopResult 渲染目标循环的终局归因。
func printLoopResult(r loop.LoopResult) {
	fmt.Printf("\n=== loop finished: %s after %d iteration(s) in %s ===\n",
		r.Outcome.String(), r.Iterations, r.Elapsed.Round(time.Millisecond))
	if r.Outcome != loop.OutcomeAchieved && strings.TrimSpace(r.LastReport.Summary) != "" {
		fmt.Printf("  last verification: %s\n", r.LastReport.Summary)
	}
}
