package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/alaindong/cogent/internal/agent"
	"github.com/alaindong/cogent/internal/contextmgr"
	"github.com/alaindong/cogent/internal/engine"
	"github.com/alaindong/cogent/internal/llm"
	"github.com/alaindong/cogent/internal/mcp"
	"github.com/alaindong/cogent/internal/memory"
	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/sandbox"
	"github.com/alaindong/cogent/internal/session"
	"github.com/alaindong/cogent/internal/tool"
	"github.com/alaindong/cogent/internal/types"
)

// dataDirName 是会话 transcript 与 trace 的运行期数据目录（.gitignore 已排除）。
const dataDirName = "data"

// defaultLLMBaseURL 是未配置 COGENT_LLM_BASE_URL 时的回退地址（对齐 .env.example）。
const defaultLLMBaseURL = "https://api.deepseek.com/v1"

// observeConfig 按 COGENT_OBSERVE_* 环境变量构造可观测配置；
// 默认 Enabled=false（零开销 no-op），显式开启后按 exporter（file/stdout/otlp）落地真实 trace。
func observeConfig() observe.Config {
	cfg := observe.Config{
		Enabled:      envBool("COGENT_OBSERVE_ENABLED", false),
		Exporter:     envStr("COGENT_TRACE_EXPORTER", "file"),
		TraceDir:     envStr("COGENT_TRACE_DIR", filepath.Join(dataDirName, "traces")),
		OTLPEndpoint: envStr("COGENT_OTLP_ENDPOINT", "localhost:4317"),
		SampleRatio:  envFloat("COGENT_TRACE_SAMPLE_RATIO", 1.0),
	}
	return cfg
}

// envStr 读取字符串环境变量，缺省时回退 def。
func envStr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// envBool 读取布尔环境变量（1/true/yes/on 视为真），缺省时回退 def。
func envBool(key string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

// envFloat 读取浮点环境变量，缺省或非法时回退 def。
func envFloat(key string, def float64) float64 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

// envInt 读取整型环境变量，缺省或非法时回退 def。
func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// sessionOutcome 把会话终止错误归一为 cogent.session 根 span 的 outcome 属性：
// 取消→cancelled、其他错误→error、无错→done。
func sessionOutcome(err error) string {
	switch {
	case err == nil:
		return "done"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "error"
	}
}

// newRootCmd 构造根命令并挂载 run/resume/mcp 子命令。
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "cogent",
		Short:         "cogent 是一个用 Go 编写的自主编码 Agent 运行时",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newRunCmd(), newResumeCmd(), newGoalCmd(), newLoopCmd(), newMCPCmd())
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
			return runREPL(cmd.Context(), replOptions{first: first, mode: m})
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

// newResumeCmd 构造 resume 子命令：从已有会话 transcript 重建上下文并续跑。
func newResumeCmd() *cobra.Command {
	var mode string
	cmd := &cobra.Command{
		Use:   "resume <session-id>",
		Short: "从中断处恢复一个已有会话并继续对话（无损接续）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := parseMode(mode)
			if err != nil {
				return err
			}
			return runREPL(cmd.Context(), replOptions{mode: m, resumeID: args[0]})
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "auto", "run mode: auto | plan | ask")
	return cmd
}

// newMCPCmd 构造 mcp 子命令：连接并自检已配置的 MCP server，列出可用的外部工具。
func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "连接并自检 .cogent/mcp.json 中配置的 MCP server，列出可用的外部工具",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runMCPCheck(cmd.Context())
		},
	}
}

// runMCPCheck 加载 MCP 配置、连接 server 并打印发现的外部工具，随后释放连接。
func runMCPCheck(ctx context.Context) error {
	wd, _ := os.Getwd()
	cfgs, err := mcp.LoadConfig(wd)
	if err != nil {
		return fmt.Errorf("load mcp config: %w", err)
	}
	if len(cfgs) == 0 {
		fmt.Printf("no MCP servers configured (expected at %s)\n", filepath.Join(wd, ".cogent", "mcp.json"))
		return nil
	}
	prov, err := observe.New(observeConfig())
	if err != nil {
		return fmt.Errorf("init observe: %w", err)
	}
	defer func() { _ = prov.Shutdown(context.Background()) }()

	mgr := mcp.NewManager(mcp.Transport(os.Getenv("COGENT_MCP_TRANSPORT")), prov.Tracer())
	mgr.Connect(ctx, cfgs)
	defer func() { _ = mgr.Close() }()

	tools := mgr.Tools()
	fmt.Printf("connected; %d MCP tool(s) available:\n", len(tools))
	for _, t := range tools {
		fmt.Printf("  - %s: %s\n", t.Name(), t.Description())
	}
	return nil
}

// replOptions 聚合 REPL 的启动选项：首轮输入、运行档位与可选的 resume 会话 ID。
type replOptions struct {
	first    string      // 非空时作为第一轮输入
	mode     engine.Mode // 运行档位
	resumeID string      // 非空时从该会话恢复
}

// runREPL 装配依赖并进入交互式对话循环；按 opts 决定新建会话或从 resumeID 恢复。
func runREPL(ctx context.Context, opts replOptions) error {
	prov, err := observe.New(observeConfig())
	if err != nil {
		return fmt.Errorf("init observe: %w", err)
	}
	defer func() { _ = prov.Shutdown(context.Background()) }()

	in := newInputReader(os.Stdin)
	prompter := newCLIPrompter(in)
	sid := opts.resumeID
	if sid == "" {
		sid = session.NewSessionID()
	}
	wd, _ := os.Getwd()
	mgr, err := buildMCPManager(ctx, wd, prov.Tracer())
	if err != nil {
		return err
	}
	defer func() { _ = mgr.Close() }()

	eng, err := buildEngine(prov, prompter, opts.mode, sid, wd, mgr.Tools())
	if err != nil {
		return err
	}

	ctx, end := prov.Tracer().Start(ctx, "cogent.session",
		observe.Attr{Key: "session.id", Value: sid},
		observe.Attr{Key: "mode", Value: opts.mode.String()},
	)
	var runErr error
	defer func() { end(runErr, observe.Attr{Key: "outcome", Value: sessionOutcome(runErr)}) }()

	slog.Info("repl started", "mode", opts.mode, "session", sid)
	fmt.Printf("cogent — session %s — type 'exit' or 'quit' (or Ctrl-C) to leave.\n", sid)
	fmt.Printf("  (resume later with: cogent resume %s)\n", sid)
	fmt.Println("  (type '/undo' to undo last turn)")
	runErr = driveREPL(ctx, eng, in, opts)
	if errors.Is(runErr, context.Canceled) {
		return nil
	}
	return runErr
}

// driveREPL 根据是否 resume 选择启动方式，随后进入共享的多轮输入循环。
func driveREPL(ctx context.Context, eng engine.Engine, in *inputReader, opts replOptions) error {
	if opts.resumeID != "" {
		fmt.Print("cogent> ")
		events, err := eng.Resume(ctx, opts.resumeID)
		if err != nil {
			return fmt.Errorf("resume: %w", err)
		}
		if err := consumeEvents(ctx, events); err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			fmt.Fprintln(os.Stderr, "\ncogent: resume error:", err)
		}
		return inputLoop(ctx, eng, in)
	}
	return replLoop(ctx, eng, in, opts.first)
}

// buildMCPManager 加载 .cogent/mcp.json 并连接配置的 MCP server（缺省配置时为空管理器，不影响运行）。
func buildMCPManager(ctx context.Context, workRoot string, tracer observe.Tracer) (*mcp.Manager, error) {
	cfgs, err := mcp.LoadConfig(workRoot)
	if err != nil {
		return nil, fmt.Errorf("load mcp config: %w", err)
	}
	mgr := mcp.NewManager(mcp.Transport(os.Getenv("COGENT_MCP_TRANSPORT")), tracer)
	mgr.Connect(ctx, cfgs)
	return mgr, nil
}

// newLLMClient 按环境变量构造 LLM 客户端（DeepSeek OpenAI 兼容接口）；密钥仅来自 env。
// 重试退避默认关闭（fail-closed），设 COGENT_LLM_RETRY_ATTEMPTS>1 后才对建流阶段的可重试错误退避。
func newLLMClient() (llm.Client, error) {
	baseURL := os.Getenv("COGENT_LLM_BASE_URL")
	if baseURL == "" {
		baseURL = defaultLLMBaseURL
	}
	llmc, err := llm.New(llm.Config{
		APIKey:  os.Getenv("DEEPSEEK_API_KEY"),
		BaseURL: baseURL,
		Retry:   llmRetryPolicy(),
	})
	if err != nil {
		return nil, fmt.Errorf("init llm: %w", err)
	}
	return llmc, nil
}

// llmRetryPolicy 按环境变量构造 LLM 建流重试策略；默认 MaxAttempts=1（不重试，向后兼容）。
func llmRetryPolicy() llm.RetryPolicy {
	attempts := envInt("COGENT_LLM_RETRY_ATTEMPTS", 1)
	if attempts <= 1 {
		return llm.RetryPolicy{}
	}
	return llm.RetryPolicy{
		MaxAttempts: attempts,
		BaseDelay:   time.Duration(envInt("COGENT_LLM_RETRY_BASE_MS", 500)) * time.Millisecond,
		MaxDelay:    time.Duration(envInt("COGENT_LLM_RETRY_MAX_MS", 10000)) * time.Millisecond,
	}
}

// buildEngine 按环境变量装配 LLM 客户端、工具池（内建 + MCP）、会话存储与执行内核。
func buildEngine(
	prov observe.Provider,
	prompter permission.Prompter,
	mode engine.Mode,
	sessionID string,
	workRoot string,
	mcpTools []tool.Tool,
) (engine.Engine, error) {
	llmc, err := newLLMClient()
	if err != nil {
		return nil, err
	}
	spawner := buildSpawner(llmc, prov, workRoot)
	eng, err := engine.New(engine.Deps{
		LLM:          llmc,
		Tools:        buildToolPool(workRoot, prompter, prov.Tracer(), mcpTools, spawner),
		Context:      contextmgr.New(),
		Memory:       memory.New(),
		MemoryWriter: memory.NewWriter(),
		Session:      session.NewStore(filepath.Join(workRoot, dataDirName)),
		SessionID:    sessionID,
		Observe:      prov,
		Snapshotter:  engine.NewGitSnapshotter(workRoot),
		Mode:         mode,
		Model:        os.Getenv("COGENT_MODEL"),
		WorkRoot:     workRoot,
		LLMTimeout:   time.Duration(envInt("COGENT_LLM_TIMEOUT_SEC", 120)) * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("init engine: %w", err)
	}
	return eng, nil
}

// buildSpawner 装配 SubAgent 派发器：用只读子工具池（read_file/list_dir/grep，
// 刻意不含 task 自身以杜绝无限递归派发），复用主任务的 LLM 客户端与 observe provider
// 以串联 trace。返回类型为 tool.Spawner 抽象，由 task 工具持有（破依赖环）。
func buildSpawner(llmc llm.Client, prov observe.Provider, workRoot string) tool.Spawner {
	subTools := tool.NewPool(
		tool.NewReadFile(workRoot),
		tool.NewListDir(workRoot),
		tool.NewGrep(workRoot),
	)
	return agent.New(engine.Deps{
		LLM:      llmc,
		Tools:    subTools,
		Observe:  prov,
		Model:    os.Getenv("COGENT_MODEL"),
		WorkRoot: workRoot,
	})
}

// buildToolPool 装配工具池：只读内建工具（含 task 派发工具）直接入池；写/执行类与全部
// MCP 外部工具经 Guard 包裹注入权限闸门与 HITL。MCP 工具排在内建之后传入，借 NewPool
// 的先到先得实现“内建优先”去重，防止外部同名工具劫持内建工具。
func buildToolPool(
	workRoot string,
	prompter permission.Prompter,
	tracer observe.Tracer,
	mcpTools []tool.Tool,
	spawner tool.Spawner,
) tool.Pool {
	policy := permission.StaticPolicy{}
	guard := func(t tool.Tool) tool.Tool { return tool.NewGuard(t, policy, prompter, tracer) }
	sb := sandbox.New(sandbox.Config{WorkRoot: workRoot, Enabled: true})
	tools := []tool.Tool{
		tool.NewReadFile(workRoot),
		tool.NewListDir(workRoot),
		tool.NewGrep(workRoot),
		tool.NewTask(spawner), // 只读派发工具：把探索子任务交给隔离子 Agent
		guard(tool.NewWriteFile(workRoot)),
		guard(tool.NewEditFile(workRoot)),
		guard(tool.NewBash(sb, tracer)),
	}
	for _, mt := range mcpTools {
		tools = append(tools, guard(mt)) // 外部不可信：统一过 permission/HITL
	}
	return tool.NewPool(tools...)
}

// buildReviewerPool 装配审查者工具池：仅只读工具（reviewer 绝不改代码，fail-closed）。
func buildReviewerPool(workRoot string) tool.Pool {
	return tool.NewPool(
		tool.NewReadFile(workRoot),
		tool.NewListDir(workRoot),
		tool.NewGrep(workRoot),
	)
}

// buildMakerPool 装配实现者工具池：只读 + 写/执行类（经 Guard 过权限闸门），供 maker 改代码。
func buildMakerPool(workRoot string, prompter permission.Prompter, tracer observe.Tracer) tool.Pool {
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

// replLoop 驱动对话循环：先处理 first（若有），再进入共享的多轮输入循环。
func replLoop(ctx context.Context, eng engine.Engine, in *inputReader, first string) error {
	if strings.TrimSpace(first) != "" {
		if err := runTurn(ctx, eng, first); err != nil {
			return err
		}
	}
	return inputLoop(ctx, eng, in)
}

// inputLoop 循环读取共享输入并逐轮执行；Ctrl-C 在等待输入时也能即时退出。
func inputLoop(ctx context.Context, eng engine.Engine, in *inputReader) error {
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
		if line == "/undo" {
			handleUndo(ctx, eng)
			continue
		}
		if err := runTurn(ctx, eng, line); err != nil {
			return err
		}
	}
}

// handleUndo 处理 /undo 命令：调用 engine.Undo 并打印撤销摘要。
func handleUndo(ctx context.Context, eng engine.Engine) {
	result, err := eng.Undo(ctx)
	if err != nil {
		if errors.Is(err, engine.ErrNothingToUndo) {
			fmt.Println("cogent> 没有可撤销的轮次")
		} else {
			fmt.Fprintf(os.Stderr, "cogent: undo error: %v\n", err)
		}
		return
	}
	if result.HasFileChanges {
		fmt.Printf("cogent> 已撤销上一轮：%s（工作区已恢复，移除 %d 条消息）\n",
			result.Summary, result.RemovedCount)
	} else {
		fmt.Printf("cogent> 已撤销对话历史：%s（本轮无文件改动，移除 %d 条消息）\n",
			result.Summary, result.RemovedCount)
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
