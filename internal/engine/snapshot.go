// Package engine 中的 snapshot.go 实现基于 git 的工作区快照管理器（Snapshotter 接口的具体实现）。
// 用于 Undo 时精确恢复工作区到某轮对话开始前的文件状态。
package engine

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// gitSnapshotter 是 Snapshotter 接口的 git 实现：
// 使用 git stash create 创建轻量快照（不影响 stash 列表），恢复时 apply 或 checkout。
type gitSnapshotter struct {
	workRoot string
}

// NewGitSnapshotter 构造一个基于 git 的工作区快照管理器。
func NewGitSnapshotter(workRoot string) Snapshotter {
	return &gitSnapshotter{workRoot: workRoot}
}

// IsGitRepo 检测 workRoot 是否在 git 仓库内。
func (g *gitSnapshotter) IsGitRepo() bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = g.workRoot
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// Take 记录当前工作区状态快照。先暂存所有改动（含未跟踪文件），
// 再使用 git stash create 创建一个不入栈的 stash 对象，最后 reset 恢复暂存区。
// 返回 SHA；若工作区无改动则返回空字符串。
func (g *gitSnapshotter) Take(ctx context.Context) (string, error) {
	// 先 add -A 把未跟踪文件也纳入，否则 stash create 不包含新文件
	add := exec.CommandContext(ctx, "git", "add", "-A")
	add.Dir = g.workRoot
	if _, err := add.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git add -A: %w", err)
	}

	cmd := exec.CommandContext(ctx, "git", "stash", "create")
	cmd.Dir = g.workRoot
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git stash create: %w", err)
	}
	sha := strings.TrimSpace(string(out))

	// reset 恢复暂存区到之前状态（不影响工作区文件）
	reset := exec.CommandContext(ctx, "git", "reset")
	reset.Dir = g.workRoot
	if _, err := reset.CombinedOutput(); err != nil {
		// reset 失败不影响快照本身的有效性，仅告警
		return sha, nil
	}
	return sha, nil
}

// Restore 恢复工作区到指定快照状态。
// 策略：先清理当前工作区（checkout + clean），再 apply 快照（若有）。
func (g *gitSnapshotter) Restore(ctx context.Context, id string) error {
	if err := g.discardAll(ctx); err != nil {
		return fmt.Errorf("discard workspace: %w", err)
	}
	if id != "" {
		cmd := exec.CommandContext(ctx, "git", "stash", "apply", id)
		cmd.Dir = g.workRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git stash apply %s: %w\n%s", id, err, string(out))
		}
	}
	return nil
}

// discardAll 丢弃工作区所有未提交改动：git checkout -- . && git clean -fd。
func (g *gitSnapshotter) discardAll(ctx context.Context) error {
	checkout := exec.CommandContext(ctx, "git", "checkout", "--", ".")
	checkout.Dir = g.workRoot
	if out, err := checkout.CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout -- .: %w\n%s", err, string(out))
	}
	clean := exec.CommandContext(ctx, "git", "clean", "-fd")
	clean.Dir = g.workRoot
	if out, err := clean.CombinedOutput(); err != nil {
		return fmt.Errorf("git clean -fd: %w\n%s", err, string(out))
	}
	return nil
}
