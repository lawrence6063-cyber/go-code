package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/alaindong/cogent/internal/permission"
)

// InputReader 是单一的 stdin 行来源，REPL 提示与权限中断（HITL）共用它。
// 两种模式互斥：TTY 时用 raw 模式交互式行编辑器（支持 @ 文件补全下拉），
// 非 TTY（管道/测试）或 raw 不可用时用后台 goroutine 逐行读取，避免多处并发读取 stdin 造成竞争。
type InputReader struct {
	lines  <-chan string // 非 TTY / 已回退：后台 Scanner 的行来源
	editor *lineEditor   // TTY：交互式行编辑器（nil 表示走逐行读取路径）
	tty    *os.File      // TTY 源；raw 交互不可用时据此懒回退到逐行读取
}

// newInputReader 启动后台逐行读取并返回行来源（非 TTY 路径，供管道与测试使用）。
func newInputReader(r io.Reader) *InputReader {
	out := make(chan string)
	go func() {
		defer close(out)
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			out <- scanner.Text()
		}
	}()
	return &InputReader{lines: out}
}

// NewTTYInputReader 构造感知终端的行来源：stdin 为终端时启用 @ 补全行编辑器（候选取自 workRoot），
// 否则退回 newInputReader 的逐行读取，保证脚本与非交互场景行为不变。
// 注意：这里只判定“是否终端”（能读 termios），真正能否进入 raw 模式在首次 readLine 时才知晓；
// 若届时发现 raw 不可用（如受限 TTY/部分 pty 拒绝写 termios，或读键即 I/O 错误），
// next 会一次性懒回退到逐行读取，而非让整个 REPL 直接退出（详见 next）。
func NewTTYInputReader(f *os.File, workRoot string) *InputReader {
	if isTerminalFD(f.Fd()) {
		return &InputReader{editor: newLineEditor(f, workRoot), tty: f}
	}
	return newInputReader(f)
}

// next 取下一行；ctx 取消或输入结束时返回 ok=false。
// TTY 路径下，若 readLine 判定 raw 交互不可用（usable=false），则一次性回退到逐行读取并重试本次读取——
// 这样即便某些终端允许读 termios 却拒绝进入 raw（或首次读键即底层 I/O 失败），REPL 仍能正常交互。
func (ir *InputReader) next(ctx context.Context) (string, bool) {
	if ir.editor != nil {
		line, ok, usable := ir.editor.readLine(ctx)
		if usable {
			return line, ok
		}
		fmt.Fprintln(os.Stderr,
			"cogent: 交互式行编辑不可用，回退到逐行输入模式（无 @ 文件补全下拉）")
		ir.fallbackToLines()
		// 回退后继续走下方逐行读取，完成本次取行。
	}
	select {
	case <-ctx.Done():
		return "", false
	case line, ok := <-ir.lines:
		return line, ok
	}
}

// fallbackToLines 关闭行编辑器并改用后台 Scanner 逐行读取 TTY（raw 不可用时的降级路径）。
func (ir *InputReader) fallbackToLines() {
	ir.editor = nil
	src := io.Reader(os.Stdin)
	if ir.tty != nil {
		src = ir.tty
	}
	ir.lines = newInputReader(src).lines
}

// cliPrompter 是 permission.Prompter 的 CLI 实现：在中断点读 stdin 完成 Approve/Edit/Reject。
// 支持会话级 per-tool "always" 自动批准：输入 A 后该工具在本会话内不再询问。
type cliPrompter struct {
	in    *InputReader
	allow map[string]bool // 工具名 → 会话级自动批准（exit 清除）
	mu    sync.Mutex      // 保护 allow（Guard 可并发调用 Ask）
}

// NewCLIPrompter 构造一个基于共享输入的 CLI 中断决策器。
func NewCLIPrompter(in *InputReader) permission.Prompter {
	return &cliPrompter{in: in, allow: make(map[string]bool)}
}

// yesPrompter 是无人值守自动批准决策器：对每个权限中断一律 Approve（不读 stdin）。
// 仅在显式设置 COGENT_YES 时启用，用于 goal/loop 等无人值守场景；
// 危险命令仍由 sandbox 确定性拦截、worktree/diff 隔离兜底，故自动批准的爆炸半径可控。
type yesPrompter struct{}

// Ask 见 permission.Prompter 接口说明：始终批准。
func (yesPrompter) Ask(_ context.Context, req permission.Interrupt) (permission.Resolution, error) {
	fmt.Printf("\n[permission:auto-approve] %s\n", Summarize(req.Tool, req.Input))
	return permission.Resolution{Action: permission.ActionApprove}, nil
}

// autoApprove 报告是否启用无人值守自动批准（COGENT_YES=1/true/yes）。
func autoApprove() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("COGENT_YES"))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// NewYesPrompter 返回无人值守自动批准决策器：对每个权限中断一律 Approve（不读 stdin）。
// 用于 Headless 场景（如 eval 批量跑分）：agent 跑在一次性工作区副本 + sandbox 上，
// 危险命令仍由 sandbox 确定性拦截兜底，故自动批准的爆炸半径可控。
func NewYesPrompter() permission.Prompter { return yesPrompter{} }

// NewPrompter 按是否无人值守选择决策器：COGENT_YES 时自动批准，否则交互式 CLI。
func NewPrompter(in *InputReader) permission.Prompter {
	if autoApprove() {
		return yesPrompter{}
	}
	return NewCLIPrompter(in)
}

// Ask 见 permission.Prompter 接口说明。
func (p *cliPrompter) Ask(ctx context.Context, req permission.Interrupt) (permission.Resolution, error) {
	// 会话级 always 短路：已标记的工具直接批准，不读 stdin。
	p.mu.Lock()
	auto := p.allow[req.Tool]
	p.mu.Unlock()
	if auto {
		fmt.Printf("\n[permission:auto-approve (always)] %s\n", Summarize(req.Tool, req.Input))
		return permission.Resolution{Action: permission.ActionApprove}, nil
	}

	fmt.Printf("\n[permission] %s\n", Summarize(req.Tool, req.Input))
	if req.Reason != "" {
		fmt.Printf("  reason: %s\n", req.Reason)
	}
	fmt.Print("  approve / always / edit / reject? [a/A/e/r] ")
	line, ok := p.in.next(ctx)
	if !ok {
		return permission.Resolution{}, ctx.Err()
	}
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	switch {
	case trimmed == "A" || lower == "always":
		p.mu.Lock()
		p.allow[req.Tool] = true
		p.mu.Unlock()
		fmt.Printf("  (always: %s auto-approved for this session)\n", req.Tool)
		return permission.Resolution{Action: permission.ActionApprove}, nil
	case lower == "a" || lower == "approve" || lower == "y" || lower == "yes":
		return permission.Resolution{Action: permission.ActionApprove}, nil
	case lower == "e" || lower == "edit":
		return p.askEdit(ctx)
	default:
		return p.askReject(ctx), nil
	}
}

// askEdit 读取修正后的 JSON 入参；输入非法 JSON 则降级为拒绝并附说明。
func (p *cliPrompter) askEdit(ctx context.Context) (permission.Resolution, error) {
	fmt.Print("  enter new JSON input: ")
	raw, ok := p.in.next(ctx)
	if !ok {
		return permission.Resolution{}, ctx.Err()
	}
	if !json.Valid([]byte(raw)) {
		return permission.Resolution{Action: permission.ActionReject, Guidance: "edited input was not valid JSON"}, nil
	}
	return permission.Resolution{Action: permission.ActionEdit, UpdatedInput: json.RawMessage(raw)}, nil
}

// askReject 读取可选的拒绝指引（回流给模型以改道）。
func (p *cliPrompter) askReject(ctx context.Context) permission.Resolution {
	fmt.Print("  guidance for the model (optional): ")
	g, _ := p.in.next(ctx)
	return permission.Resolution{Action: permission.ActionReject, Guidance: strings.TrimSpace(g)}
}
