package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/alaindong/cogent/internal/engine"
)

// driveREPL 根据是否 resume 选择启动方式，随后进入共享的多轮输入循环。
func driveREPL(ctx context.Context, eng engine.Engine, in *InputReader, opts REPLOptions, bar *StatusBar) error {
	if opts.ResumeID != "" {
		events, err := eng.Resume(ctx, opts.ResumeID)
		if err != nil {
			return fmt.Errorf("resume: %w", err)
		}
		if err := consumeEvents(ctx, events, "cogent> "); err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			fmt.Fprintln(os.Stderr, "\ncogent: resume error:", err)
		}
		return inputLoop(ctx, eng, in, bar)
	}
	return replLoop(ctx, eng, in, opts.First, bar)
}

// replLoop 驱动对话循环：先处理 first（若有），再进入共享的多轮输入循环。
func replLoop(ctx context.Context, eng engine.Engine, in *InputReader, first string, bar *StatusBar) error {
	if strings.TrimSpace(first) != "" {
		if err := runTurn(ctx, eng, first); err != nil {
			return err
		}
	}
	return inputLoop(ctx, eng, in, bar)
}

// inputLoop 循环读取共享输入并逐轮执行。
// 在 REPL 循环内自行管理 SIGINT：等待输入时由 raw mode 双击 Ctrl-C 退出；
// 模型执行时 SIGINT 仅中断当前 turn（不退出整个 REPL）。
func inputLoop(ctx context.Context, eng engine.Engine, in *InputReader, bar *StatusBar) error {
	// 接管 SIGINT：先 Reset 解除 main.go NotifyContext 对 SIGINT 的监听（防止其 cancel 顶层 ctx），
	// 再用本地 channel 独占管理 SIGINT——模型执行时仅中断当前 turn，等待输入时由 raw mode 处理。
	signal.Reset(syscall.SIGINT)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT)
	defer signal.Stop(sigCh)

	for {
		if bar != nil && in.editor != nil {
			fmt.Print(bar.render())
		}
		fmt.Print("\nyou> ")

		// 等待输入前，排空 sigCh 中可能遗留的信号（来自上一轮 runTurn 的残留 SIGINT）。
		drainSignals(sigCh)

		line, ok := in.next(ctx)
		if !ok {
			fmt.Println()
			// 如果顶层 ctx 已被 cancel（SIGTERM 等），返回其 err；
			// 否则是 readLine 双击 Ctrl-C 返回的 ok=false，正常退出。
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return nil
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" || line == "/exit" {
			return nil
		}
		if strings.HasPrefix(line, "/") {
			handleSlashCommand(ctx, eng, line)
			continue
		}
		// 模型执行：用 per-turn ctx，SIGINT 只取消当前 turn。
		if err := runTurnInterruptible(ctx, eng, line, sigCh); err != nil {
			return err
		}
	}
}

// runTurnInterruptible 执行一轮对话，SIGINT 仅取消本轮（REPL 继续）。
func runTurnInterruptible(parent context.Context, eng engine.Engine, line string, sigCh <-chan os.Signal) error {
	turnCtx, cancel := context.WithCancel(parent)
	defer cancel()

	// 监听 SIGINT：收到时 cancel 本轮 turnCtx。
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-done:
		}
	}()

	events, err := eng.Run(turnCtx, line)
	if err != nil {
		if errors.Is(err, context.Canceled) && parent.Err() == nil {
			// 仅本轮被 SIGINT 中断，REPL 继续。
			fmt.Fprintln(os.Stderr, "\n[interrupted]")
			return nil
		}
		return fmt.Errorf("run: %w", err)
	}
	if err := consumeEvents(turnCtx, events, "cogent> "); err != nil {
		if errors.Is(err, context.Canceled) && parent.Err() == nil {
			fmt.Fprintln(os.Stderr, "\n[interrupted]")
			return nil
		}
		if !errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "\ncogent: turn error:", err)
		}
	}
	return nil
}

// drainSignals 排空 signal channel 中积压的信号，防止误触发。
func drainSignals(ch <-chan os.Signal) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// runTurn 执行一轮对话：调用 engine 并流式渲染回复。
func runTurn(ctx context.Context, eng engine.Engine, line string) error {
	events, err := eng.Run(ctx, line)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}
	if err := consumeEvents(ctx, events, "cogent> "); err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		fmt.Fprintln(os.Stderr, "\ncogent: turn error:", err)
	}
	return nil
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
