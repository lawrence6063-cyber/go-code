package main

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

// inputReader 是单一的 stdin 行来源：后台 goroutine 逐行读取，按需串行交给请求者。
// REPL 提示与权限中断（HITL）共用它，避免多处并发读取 stdin 造成竞争。
type inputReader struct {
	lines <-chan string
}

// newInputReader 启动后台读取并返回行来源。
func newInputReader(r io.Reader) *inputReader {
	out := make(chan string)
	go func() {
		defer close(out)
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			out <- scanner.Text()
		}
	}()
	return &inputReader{lines: out}
}

// next 取下一行；ctx 取消或输入结束时返回 ok=false。
func (ir *inputReader) next(ctx context.Context) (string, bool) {
	select {
	case <-ctx.Done():
		return "", false
	case line, ok := <-ir.lines:
		return line, ok
	}
}

// cliPrompter 是 permission.Prompter 的 CLI 实现：在中断点读 stdin 完成 Approve/Edit/Reject。
// 支持会话级 per-tool "always" 自动批准：输入 A 后该工具在本会话内不再询问。
type cliPrompter struct {
	in    *inputReader
	allow map[string]bool // 工具名 → 会话级自动批准（exit 清除）
	mu    sync.Mutex      // 保护 allow（Guard 可并发调用 Ask）
}

// newCLIPrompter 构造一个基于共享输入的 CLI 中断决策器。
func newCLIPrompter(in *inputReader) permission.Prompter {
	return &cliPrompter{in: in, allow: make(map[string]bool)}
}

// yesPrompter 是无人值守自动批准决策器：对每个权限中断一律 Approve（不读 stdin）。
// 仅在显式设置 COGENT_YES 时启用，用于 goal/loop 等无人值守场景；
// 危险命令仍由 sandbox 确定性拦截、worktree/diff 隔离兜底，故自动批准的爆炸半径可控。
type yesPrompter struct{}

// Ask 见 permission.Prompter 接口说明：始终批准。
func (yesPrompter) Ask(_ context.Context, req permission.Interrupt) (permission.Resolution, error) {
	fmt.Printf("\n[permission:auto-approve] tool %q\n  input: %s\n", req.Tool, string(req.Input))
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

// newPrompter 按是否无人值守选择决策器：COGENT_YES 时自动批准，否则交互式 CLI。
func newPrompter(in *inputReader) permission.Prompter {
	if autoApprove() {
		return yesPrompter{}
	}
	return newCLIPrompter(in)
}

// Ask 见 permission.Prompter 接口说明。
func (p *cliPrompter) Ask(ctx context.Context, req permission.Interrupt) (permission.Resolution, error) {
	// 会话级 always 短路：已标记的工具直接批准，不读 stdin。
	p.mu.Lock()
	auto := p.allow[req.Tool]
	p.mu.Unlock()
	if auto {
		fmt.Printf("\n[permission:auto-approve (always)] tool %q\n  input: %s\n", req.Tool, string(req.Input))
		return permission.Resolution{Action: permission.ActionApprove}, nil
	}

	fmt.Printf("\n[permission] tool %q requests execution:\n  input: %s\n", req.Tool, string(req.Input))
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
