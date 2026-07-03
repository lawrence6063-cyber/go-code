package tui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/alaindong/cogent/internal/tui/render"
	"github.com/alaindong/cogent/internal/types"
)

// spinnerInterval 是 spinner 动画的刷新周期。
const spinnerInterval = 100 * time.Millisecond

// maxErrorLines 是错误结果保留展示的关键行数上限（超出折叠）。
const maxErrorLines = 3

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

// streamRenderer 消费单轮事件流并渲染到 w，采用类 Claude Code 的分区排版：
// 首个可见输出前显示 spinner；模型正文经流式 Markdown 渲染；工具调用折叠为单行摘要
// `● 工具名 参数`，其结果收敛为单行状态 `⎿ …`；正文与工具块之间以空行分区。
// 非 TTY（rich=false）退回纯文本，行为与管道/脚本兼容。
type streamRenderer struct {
	w           io.Writer
	rich        bool
	prompt      string
	md          render.StreamMarkdown
	spin        *spinner
	promptShown bool
	inTool      bool // 是否处于「工具已开始、结果未到」区间（用于分区与进度抑制）
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
		r.writeText(ev.Text)
	case types.EventToolStart:
		r.startTool(ev.ToolUse)
	case types.EventToolResult:
		r.finishTool(ev.Result)
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

// writeText 渲染模型正文增量：工具区间内到达的进度文本被抑制（不与正文混流），
// 正文文本经流式 Markdown 渲染逐行输出。
func (r *streamRenderer) writeText(text string) {
	if text == "" {
		return
	}
	if r.inTool {
		// 工具执行期间的进度/子 Agent 文本不并入主正文，避免刷屏；结果由 finishTool 汇总。
		return
	}
	r.beginOutput()
	fmt.Fprint(r.w, r.md.Write(text))
}

// startTool 打印工具调用的单行摘要（`● 工具名 参数`）并进入工具区间；
// 先 flush 正文并插入空行，与上文视觉分区。
func (r *streamRenderer) startTool(tu *types.ToolUseBlock) {
	r.beginOutput()
	fmt.Fprint(r.w, r.md.Flush())
	if tu == nil {
		r.inTool = true
		return
	}
	fmt.Fprintf(r.w, "\n%s\n", render.Cyan("● "+Summarize(tu.Name, tu.Input)))
	r.inTool = true
}

// finishTool 打印工具结果的单行状态（`⎿ …`）并退出工具区间。
func (r *streamRenderer) finishTool(res *types.ToolResult) {
	r.beginOutput()
	r.inTool = false
	if res == nil {
		return
	}
	fmt.Fprintf(r.w, "  %s %s\n", render.Faint("⎿"), resultStatus(res))
}

// resultStatus 把工具结果收敛为一行状态：有 diff 则给增删统计，错误标红并保留关键前几行，
// 普通结果按内容规模给「单行内容」或「N 行」摘要，不再全量刷屏。
func resultStatus(res *types.ToolResult) string {
	if res.Diff != "" {
		add, del := diffStat(res.Diff)
		return fmt.Sprintf("%s %s %s",
			render.Green("✓"),
			render.Green(fmt.Sprintf("+%d", add)),
			render.Red(fmt.Sprintf("-%d", del)))
	}
	content := strings.TrimRight(res.Content, "\n")
	if res.IsError {
		return render.Red("✗ " + collapseError(content))
	}
	if content == "" {
		return render.Green("✓") + " " + render.Faint("done")
	}
	if n := strings.Count(content, "\n") + 1; n > 1 {
		return render.Green("✓") + " " + render.Faint(fmt.Sprintf("%d lines", n))
	}
	return render.Green("✓") + " " + render.Faint(render.Truncate(content, 72))
}

// collapseError 保留错误结果的前若干关键行并提示剩余行数（单行则直接截断）。
func collapseError(content string) string {
	if content == "" {
		return "error"
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= maxErrorLines {
		return render.Truncate(strings.Join(lines, " "), 120)
	}
	shown := render.Truncate(strings.Join(lines[:maxErrorLines], " "), 120)
	return fmt.Sprintf("%s (+%d more lines)", shown, len(lines)-maxErrorLines)
}

// diffStat 统计 unified diff 的新增/删除行数（忽略 +++/---/@@ 头部）。
func diffStat(diff string) (add, del int) {
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			continue
		case strings.HasPrefix(line, "+"):
			add++
		case strings.HasPrefix(line, "-"):
			del++
		}
	}
	return add, del
}

// handlePlain 是非 TTY 纯文本路径，保持简洁可管道消费的输出（工具以标记行呈现，结果折叠为规模）。
func (r *streamRenderer) handlePlain(ev types.StreamEvent) error {
	if !r.promptShown {
		fmt.Fprint(r.w, r.prompt)
		r.promptShown = true
	}
	switch ev.Type {
	case types.EventText:
		if !r.inTool {
			fmt.Fprint(r.w, ev.Text)
		}
	case types.EventToolStart:
		r.inTool = true
		if ev.ToolUse != nil {
			fmt.Fprintf(r.w, "\n[tool] %s\n", Summarize(ev.ToolUse.Name, ev.ToolUse.Input))
		}
	case types.EventToolResult:
		r.inTool = false
		if ev.Result != nil {
			fmt.Fprintf(r.w, "[result] %s\n", plainResult(ev.Result))
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

// plainResult 为非 TTY 路径给出无 ANSI 的简明结果状态。
func plainResult(res *types.ToolResult) string {
	if res.Diff != "" {
		add, del := diffStat(res.Diff)
		return fmt.Sprintf("+%d -%d", add, del)
	}
	content := strings.TrimRight(res.Content, "\n")
	if res.IsError {
		return "error: " + collapseError(content)
	}
	if content == "" {
		return "done"
	}
	if n := strings.Count(content, "\n") + 1; n > 1 {
		return fmt.Sprintf("%d lines", n)
	}
	return content
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

// printEvent 以纯文本方式把单个事件回显到 stdout，供内层子 Agent/外层循环事件的轻量渲染复用。
func printEvent(ev types.StreamEvent) error {
	r := &streamRenderer{w: os.Stdout, rich: false, promptShown: true}
	return r.handlePlain(ev)
}
