package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/alaindong/cogent/internal/render"
	"github.com/alaindong/cogent/internal/types"
)

// spinnerInterval 是 spinner 动画的刷新周期。
const spinnerInterval = 100 * time.Millisecond

// maxResultLines 是工具结果折叠阈值：超过则只显示前若干行并提示剩余行数。
const maxResultLines = 20

// spinnerFrames 是等待首个输出时的 spinner 动画帧（Braille 点阵，等宽 1 列）。
var spinnerFrames = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

// spinner 在等待首个可见输出期间原地刷新加载动画（cooked 模式，用回车覆盖当前行）。
type spinner struct {
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// startSpinner 启动一个后台刷新的 spinner，显示 label；调用方须在首个输出前 Stop。
func startSpinner(w io.Writer, label string) *spinner {
	s := &spinner{stop: make(chan struct{}), done: make(chan struct{})}
	go s.run(w, label)
	return s
}

// run 是 spinner 的动画循环，直到收到 stop 信号。
func (s *spinner) run(w io.Writer, label string) {
	defer close(s.done)
	t := time.NewTicker(spinnerInterval)
	defer t.Stop()
	for i := 0; ; i++ {
		select {
		case <-s.stop:
			return
		case <-t.C:
			fmt.Fprintf(w, "\r%c %s", spinnerFrames[i%len(spinnerFrames)], label)
		}
	}
}

// Stop 停止动画并清除当前行（回到行首并清到行尾）；幂等，可安全多次调用。
func (s *spinner) Stop(w io.Writer) {
	s.once.Do(func() {
		close(s.stop)
		<-s.done
		fmt.Fprint(w, "\r\x1b[K")
	})
}

// streamRenderer 消费单轮事件流并渲染到 w：TTY（rich）下首个可见输出前显示 spinner、
// 助手文本经流式 Markdown 渲染、工具结果的 diff 着色展示；非 TTY 退回纯文本，行为与旧版一致。
type streamRenderer struct {
	w           io.Writer
	rich        bool
	prompt      string
	md          render.StreamMarkdown
	spin        *spinner
	promptShown bool
}

// newStreamRenderer 构造渲染器；rich=true 时启动 spinner，prompt 在首个可见输出前打印一次。
func newStreamRenderer(w io.Writer, rich bool, prompt string) *streamRenderer {
	r := &streamRenderer{w: w, rich: rich, prompt: prompt}
	if rich {
		r.spin = startSpinner(w, "thinking…")
	}
	return r
}

// stopSpinner 幂等停止 spinner（若有）。
func (r *streamRenderer) stopSpinner() {
	if r.spin != nil {
		r.spin.Stop(r.w)
		r.spin = nil
	}
}

// beginOutput 在首个可见输出前收尾 spinner 并打印一次 prompt 前缀。
func (r *streamRenderer) beginOutput() {
	r.stopSpinner()
	if !r.promptShown {
		fmt.Fprint(r.w, r.prompt)
		r.promptShown = true
	}
}

// handle 渲染单个事件；EventError 返回其错误由上层处理。
func (r *streamRenderer) handle(ev types.StreamEvent) error {
	if !r.rich {
		return r.handlePlain(ev)
	}
	return r.handleRich(ev)
}

// handleRich 是 TTY 富渲染路径。
func (r *streamRenderer) handleRich(ev types.StreamEvent) error {
	switch ev.Type {
	case types.EventText:
		if ev.Text == "" {
			return nil
		}
		r.beginOutput()
		fmt.Fprint(r.w, r.md.Write(ev.Text))
	case types.EventToolStart:
		r.beginOutput()
		fmt.Fprint(r.w, r.md.Flush())
		if ev.ToolUse != nil {
			fmt.Fprintf(r.w, "\n%s\n", render.Cyan("● "+ev.ToolUse.Name))
		}
	case types.EventToolResult:
		r.beginOutput()
		if ev.Result != nil {
			r.printResult(ev.Result)
		}
	case types.EventCompacted:
		r.beginOutput()
		fmt.Fprintln(r.w, render.Faint("[context compacted]"))
	case types.EventDone:
		r.beginOutput()
		fmt.Fprint(r.w, r.md.Flush())
		fmt.Fprintln(r.w)
	case types.EventError:
		r.beginOutput()
		fmt.Fprint(r.w, r.md.Flush())
		return ev.Err
	default:
		slog.Warn("unknown event type", "type", int(ev.Type))
	}
	return nil
}

// printResult 渲染工具结果：有 diff 则着色展示，错误标红，普通结果折叠长文本。
func (r *streamRenderer) printResult(res *types.ToolResult) {
	if res.Diff != "" {
		fmt.Fprintln(r.w, render.ColorizeDiff(res.Diff))
		return
	}
	if res.IsError {
		fmt.Fprintf(r.w, "%s %s\n", render.Red("✗"), res.Content)
		return
	}
	fmt.Fprintf(r.w, "%s %s\n", render.Faint("↳"), collapseResult(res.Content))
}

// collapseResult 折叠超长结果：超过 maxResultLines 行则截断并提示剩余行数。
func collapseResult(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= maxResultLines {
		return s
	}
	shown := strings.Join(lines[:maxResultLines], "\n")
	return shown + render.Faint(fmt.Sprintf("\n… (%d more lines)", len(lines)-maxResultLines))
}

// handlePlain 是非 TTY 纯文本路径，保持与旧 printEvent 一致的输出以确保管道/测试零回归。
func (r *streamRenderer) handlePlain(ev types.StreamEvent) error {
	if !r.promptShown {
		fmt.Fprint(r.w, r.prompt)
		r.promptShown = true
	}
	switch ev.Type {
	case types.EventText:
		fmt.Fprint(r.w, ev.Text)
	case types.EventToolStart:
		if ev.ToolUse != nil {
			fmt.Fprintf(r.w, "\n[tool] %s\n", ev.ToolUse.Name)
		}
	case types.EventToolResult:
		if ev.Result != nil {
			fmt.Fprintf(r.w, "\n[result] %s\n", ev.Result.Content)
		}
	case types.EventCompacted:
		fmt.Fprintln(r.w, "\n[context compacted]")
	case types.EventDone:
		fmt.Fprintln(r.w)
	case types.EventError:
		return ev.Err
	default:
		slog.Warn("unknown event type", "type", int(ev.Type))
	}
	return nil
}

// consumeEvents 消费事件流并渲染到 stdout；prompt 在首个可见输出前打印一次。
// ctx 取消时收尾 spinner 并安全退出。
func consumeEvents(ctx context.Context, events <-chan types.StreamEvent, prompt string) error {
	r := newStreamRenderer(os.Stdout, isTerminalFD(os.Stdout.Fd()), prompt)
	defer r.stopSpinner()
	for {
		select {
		case <-ctx.Done():
			r.stopSpinner()
			fmt.Println("\n[interrupted]")
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			if err := r.handle(ev); err != nil {
				return err
			}
		}
	}
}

// printEvent 以纯文本方式把单个事件回显到 stdout，供内层子 Agent 事件的轻量渲染复用。
func printEvent(ev types.StreamEvent) error {
	r := &streamRenderer{w: os.Stdout, rich: false, promptShown: true}
	return r.handlePlain(ev)
}
