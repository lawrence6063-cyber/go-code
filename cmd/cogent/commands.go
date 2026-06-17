package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/alaindong/cogent/internal/contextmgr"
	"github.com/alaindong/cogent/internal/engine"
	"github.com/alaindong/cogent/internal/llm"
	"github.com/alaindong/cogent/internal/memory"
	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/sandbox"
	"github.com/alaindong/cogent/internal/tool"
	"github.com/alaindong/cogent/internal/types"
)

// defaultLLMBaseURL 是未配置 COGENT_LLM_BASE_URL 时的回退地址（对齐 .env.example）。
const defaultLLMBaseURL = "https://api.deepseek.com/v1"

// newRootCmd 构造根命令并挂载 run/resume/mcp 子命令。
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "cogent",
		Short:         "cogent 是一个用 Go 编写的自主编码 Agent 运行时",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newRunCmd(), newResumeCmd(), newMCPCmd())
	return root
}

// newRunCmd 构造 run 子命令：进入交互式 REPL 与 cogent 流式多轮对话。
// 可选地把首个参数作为第一轮输入；--mode 选择运行档位（auto/plan/ask）。
func newRunCmd() *cobra.Command {
	var mode string
	cmd := &cobra.Command{
		Use:   "run [task]",
		Short: "进入交互式对话，与 cogent 流式多轮对话（exit/quit 或 Ctrl-C 退出）",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := parseMode(mode)
			if err != nil {
				return err
			}
			first := ""
			if len(args) == 1 {
				first = args[0]
			}
			return runREPL(cmd.Context(), first, m)
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "auto", "run mode: auto | plan | ask")
	return cmd
}

// parseMode 把 --mode 字符串解析为 engine.Mode。
func parseMode(s string) (engine.Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return engine.ModeAuto, nil
	case "plan":
		return engine.ModePlan, nil
	case "ask":
		return engine.ModeAsk, nil
	default:
		return engine.ModeAuto, fmt.Errorf("unknown mode %q (want auto|plan|ask)", s)
	}
}

// newResumeCmd 构造 resume 子命令（Phase 5 实现，当前占位）。
func newResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume [session-id]",
		Short: "从中断处恢复一个已有会话（尚未实现）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("resume not implemented yet (planned for Phase 5)")
		},
	}
}

// newMCPCmd 构造 mcp 子命令（Phase 6 实现，当前占位）。
func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "管理 MCP 外部工具连接（尚未实现）",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("mcp not implemented yet (planned for Phase 6)")
		},
	}
}

// runREPL 装配依赖并进入交互式对话循环；first 非空时作为第一轮输入，mode 选择运行档位。
func runREPL(ctx context.Context, first string, mode engine.Mode) error {
	prov, err := observe.New(observe.Config{Enabled: false})
	if err != nil {
		return fmt.Errorf("init observe: %w", err)
	}
	defer func() { _ = prov.Shutdown(context.Background()) }()

	in := newInputReader(os.Stdin)
	prompter := newCLIPrompter(in)
	eng, err := buildEngine(prov, prompter, mode)
	if err != nil {
		return err
	}

	ctx, end := prov.Tracer().Start(ctx, "cogent.session")
	var runErr error
	defer func() { end(runErr) }()

	slog.Info("repl started", "mode", mode)
	fmt.Println("cogent — type 'exit' or 'quit' (or Ctrl-C) to leave.")
	runErr = replLoop(ctx, eng, in, first)
	if errors.Is(runErr, context.Canceled) {
		return nil
	}
	return runErr
}

// buildEngine 按环境变量装配 LLM 客户端、工具池与执行内核。
func buildEngine(prov observe.Provider, prompter permission.Prompter, mode engine.Mode) (engine.Engine, error) {
	baseURL := os.Getenv("COGENT_LLM_BASE_URL")
	if baseURL == "" {
		baseURL = defaultLLMBaseURL
	}
	llmc, err := llm.New(llm.Config{APIKey: os.Getenv("DEEPSEEK_API_KEY"), BaseURL: baseURL})
	if err != nil {
		return nil, fmt.Errorf("init llm: %w", err)
	}
	wd, _ := os.Getwd()
	eng, err := engine.New(engine.Deps{
		LLM:          llmc,
		Tools:        buildToolPool(wd, prompter, prov.Tracer()),
		Context:      contextmgr.New(),
		Memory:       memory.New(),
		MemoryWriter: memory.NewWriter(),
		Observe:      prov,
		Mode:         mode,
		Model:        os.Getenv("COGENT_MODEL"),
		WorkRoot:     wd,
	})
	if err != nil {
		return nil, fmt.Errorf("init engine: %w", err)
	}
	return eng, nil
}

// buildToolPool 装配内建工具池：只读类直接入池；写/执行类经 Guard 注入权限闸门与 HITL。
// bash 经统一的 sandbox.Sandbox 执行（危险拦截 + 受限环境 + 工作目录约束 + 超时 + 执行后清理）。
func buildToolPool(workRoot string, prompter permission.Prompter, tracer observe.Tracer) tool.Pool {
	policy := permission.StaticPolicy{}
	guard := func(t tool.Tool) tool.Tool { return tool.NewGuard(t, policy, prompter, tracer) }
	sb := sandbox.New(sandbox.Config{WorkRoot: workRoot, Enabled: true})
	return tool.NewPool(
		tool.NewReadFile(workRoot),
		tool.NewListDir(workRoot),
		tool.NewGrep(workRoot),
		guard(tool.NewWriteFile(workRoot)),
		guard(tool.NewEditFile(workRoot)),
		guard(tool.NewBash(sb, tracer)),
	)
}

// replLoop 驱动对话循环：先处理 first（若有），再循环读取共享输入。
// Ctrl-C 在等待输入时也能即时退出（共用 inputReader）。
func replLoop(ctx context.Context, eng engine.Engine, in *inputReader, first string) error {
	if strings.TrimSpace(first) != "" {
		if err := runTurn(ctx, eng, first); err != nil {
			return err
		}
	}
	for {
		fmt.Print("\nyou> ")
		line, ok := in.next(ctx)
		if !ok {
			fmt.Println()
			return ctx.Err()
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			return nil
		}
		if err := runTurn(ctx, eng, line); err != nil {
			return err
		}
	}
}

// runTurn 执行一轮对话：调用 engine 并流式渲染回复。
func runTurn(ctx context.Context, eng engine.Engine, line string) error {
	events, err := eng.Run(ctx, line)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}
	fmt.Print("cogent> ")
	if err := consumeEvents(ctx, events); err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		fmt.Fprintln(os.Stderr, "\ncogent: turn error:", err)
	}
	return nil
}

// consumeEvents 消费事件流并打印到 stdout；ctx 取消时安全收尾。
func consumeEvents(ctx context.Context, events <-chan types.StreamEvent) error {
	for {
		select {
		case <-ctx.Done():
			fmt.Println("\n[interrupted]")
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			if err := printEvent(ev); err != nil {
				return err
			}
		}
	}
}

// printEvent 将单个事件渲染到 stdout。
func printEvent(ev types.StreamEvent) error {
	switch ev.Type {
	case types.EventText:
		fmt.Print(ev.Text)
	case types.EventToolStart:
		if ev.ToolUse != nil {
			fmt.Printf("\n[tool] %s\n", ev.ToolUse.Name)
		}
	case types.EventToolResult:
		if ev.Result != nil {
			fmt.Printf("\n[result] %s\n", ev.Result.Content)
		}
	case types.EventCompacted:
		fmt.Println("\n[context compacted]")
	case types.EventDone:
		fmt.Println()
	case types.EventError:
		return ev.Err
	default:
		slog.Warn("unknown event type", "type", int(ev.Type))
	}
	return nil
}
