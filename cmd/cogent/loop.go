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
	"github.com/alaindong/cogent/internal/progress"
	"github.com/alaindong/cogent/internal/session"
)

// defaultLoopInterval 是未配置 --interval 且非文件监听时的默认心跳间隔。
const defaultLoopInterval = 10 * time.Minute

// loopOptions 聚合 loop 守护子命令的运行选项。
type loopOptions struct {
	intent       string        // 目标意图（来自位置参数或 --goal-file）
	goalFile     string        // 目标文件路径（其内容作为意图）
	verifyScript string        // 验收脚本路径
	mode         engine.Mode   // 运行档位
	review       bool          // 是否启用 maker/reviewer 双角色
	worktree     bool          // 是否用 git worktree 暂存落盘
	watch        string        // 非空则文件变更触发（监听根目录）
	interval     time.Duration // 定时触发间隔（watch 为空时生效）
	budget       loop.Budget   // 三重预算护栏
	maxSteps     int           // 单轮 ReAct 最大轮数（0 = 走 env/默认）
}

// newLoopCmd 构造 loop 守护子命令：被定时（--interval）或文件变更（--watch）触发，
// 反复跑目标循环直到达标或撞预算，并把每轮结局写入 .cogent/progress.md 看板。Ctrl-C 优雅停。
func newLoopCmd() *cobra.Command {
	var (
		mode, verify, goalFile, watch string
		review, useWorktree           bool
		interval, wall                time.Duration
		maxIter                       int
		maxCost                       float64
		maxSteps                      int
	)
	cmd := &cobra.Command{
		Use:   "loop [intent]",
		Short: "守护进程：定时/文件变更触发，持续跑目标循环并把进度写入 progress.md（Ctrl-C 优雅停）",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := parseMode(mode)
			if err != nil {
				return err
			}
			intent := ""
			if len(args) == 1 {
				intent = args[0]
			}
			return runLoopCmd(cmd.Context(), loopOptions{
				intent: intent, goalFile: goalFile, verifyScript: verify, mode: m,
				review: review, worktree: useWorktree,
				watch: watch, interval: interval,
				budget:   loop.Budget{MaxIterations: maxIter, MaxCostUSD: maxCost, MaxWallClock: wall},
				maxSteps: maxSteps,
			})
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "auto", "run mode: auto | plan | ask")
	cmd.Flags().StringVar(&goalFile, "goal-file", "", "目标文件路径（内容作为目标意图，覆盖位置参数）")
	cmd.Flags().StringVar(&verify, "verify", "", "验收脚本路径（退出码 0 = 目标达成）")
	cmd.Flags().BoolVar(&review, "review", false, "启用 maker/reviewer 双角色")
	cmd.Flags().BoolVar(&useWorktree, "worktree", false, "双角色落盘用 git worktree 暂存（通过才 Merge，物理隔离；隐含 --review）")
	cmd.Flags().StringVar(&watch, "watch", "", "监听该目录的文件变更触发（留空则用 --interval 定时触发）")
	cmd.Flags().DurationVar(&interval, "interval", defaultLoopInterval, "定时触发间隔（如 10m）")
	cmd.Flags().IntVar(&maxIter, "max-iterations", 0, "外层循环最大轮数（0 = 保守默认 8）")
	cmd.Flags().Float64Var(&maxCost, "max-cost", 0, "累计 LLM 成本上限（美元，0 = 不限）")
	cmd.Flags().DurationVar(&wall, "max-wallclock", 0, "单次目标循环墙钟上限（如 15m，0 = 不限）")
	cmd.Flags().IntVar(&maxSteps, "max-steps", 0, "单轮 ReAct 最大轮数（0 = 走 COGENT_MAX_REACT_STEPS env 或默认 50）")
	return cmd
}

// runLoopCmd 解析目标意图、装配编排器与触发源，启动守护循环直到 Ctrl-C 优雅停。
func runLoopCmd(ctx context.Context, opts loopOptions) error {
	intent, err := resolveIntent(opts)
	if err != nil {
		return err
	}
	prov, err := observe.New(observeConfig())
	if err != nil {
		return fmt.Errorf("init observe: %w", err)
	}
	defer func() { _ = prov.Shutdown(context.Background()) }()

	in := newInputReader(os.Stdin)
	prompter := newPrompter(in)
	wd, _ := os.Getwd()
	sid := session.NewSessionID()

	orch, cleanup, err := buildOrchestrator(ctx, prov, prompter, opts.mode, sid, wd, opts.review, opts.worktree, opts.maxSteps)
	if err != nil {
		return err
	}
	defer cleanup()

	daemon := &loop.Daemon{
		Trigger: triggerFor(opts, wd),
		Orch:    orch,
		Board:   progress.NewBoard(),
		Tracer:  prov.Tracer(),
	}
	printLoopBanner(intent, opts)
	runErr := daemon.Run(ctx, func(loop.TriggerSignal) loop.Goal {
		return loop.Goal{
			Intent:   augmentWithSkills(ctx, wd, intent),
			WorkRoot: wd,
			Verifier: buildVerifier(opts.verifyScript, wd),
			Budget:   opts.budget,
		}
	})
	if errors.Is(runErr, context.Canceled) {
		fmt.Println("\n[loop stopped]")
		return nil
	}
	return runErr
}

// resolveIntent 解析目标意图：--goal-file 优先（读其内容），否则用位置参数。
func resolveIntent(opts loopOptions) (string, error) {
	if strings.TrimSpace(opts.goalFile) != "" {
		data, err := os.ReadFile(opts.goalFile)
		if err != nil {
			return "", fmt.Errorf("read goal file: %w", err)
		}
		if intent := strings.TrimSpace(string(data)); intent != "" {
			return intent, nil
		}
		return "", errors.New("goal file is empty")
	}
	if intent := strings.TrimSpace(opts.intent); intent != "" {
		return intent, nil
	}
	return "", errors.New("missing goal: provide an intent arg or --goal-file")
}

// triggerFor 按选项选择触发源：指定 --watch 则文件变更触发，否则定时触发。
func triggerFor(opts loopOptions, workRoot string) loop.Trigger {
	if strings.TrimSpace(opts.watch) != "" {
		return loop.FSWatchTrigger{WorkRoot: opts.watch}
	}
	interval := opts.interval
	if interval <= 0 {
		interval = defaultLoopInterval
	}
	return loop.CronTrigger{Interval: interval}
}

// printLoopBanner 打印守护进程启动横幅。
func printLoopBanner(intent string, opts loopOptions) {
	fmt.Println("cogent loop — autonomous daemon (Ctrl-C to stop)")
	fmt.Printf("  intent  : %s\n", intent)
	if strings.TrimSpace(opts.watch) != "" {
		fmt.Printf("  trigger : fswatch %s\n", opts.watch)
	} else {
		interval := opts.interval
		if interval <= 0 {
			interval = defaultLoopInterval
		}
		fmt.Printf("  trigger : cron every %s\n", interval)
	}
	fmt.Println("  progress: .cogent/progress.md")
}
