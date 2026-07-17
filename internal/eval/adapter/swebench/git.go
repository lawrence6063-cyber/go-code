// 本文件封装 swebench Adapter 所需的 git 操作（EVAL_SPEC §5.2）。
// 均直接调用 git 可执行文件（exec.CommandContext，不经 shell），避免注入并守全局安全规则（RCE）。
package swebench

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// gitResult 是一次 git 调用的结果。
type gitResult struct {
	stdout   string
	stderr   string
	exitCode int
}

// runGit 在 dir 目录执行一次 git 命令（args 为参数，不经 shell 解析）。
// 返回捕获的 stdout/stderr 与退出码；启动失败或非退出错误时 err 非 nil。
func runGit(ctx context.Context, dir string, args ...string) (gitResult, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	res := gitResult{stdout: out.String(), stderr: errBuf.String()}
	if err == nil {
		return res, nil
	}
	var exitErr *exec.ExitError
	if ok := asExitError(err, &exitErr); ok {
		res.exitCode = exitErr.ExitCode()
		return res, nil // 非 0 退出不算调用失败，交调用方按 exitCode 判断
	}
	res.exitCode = -1
	return res, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
}

// asExitError 报告 err 是否为 *exec.ExitError（命令跑起来了但退出码非 0），并回填指针。
func asExitError(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*target = ee
		return true
	}
	return false
}

// gitAvailable 报告 git 是否在 PATH 上（Prepare 阶段 fail-fast 用）。
func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// cloneAt 从本地镜像 mirror 克隆到 dst 并检出 base 提交，得到隔离的工作区副本。
// 用本地镜像而非联网 clone：守「Adapter 不联网」不变量（数据集与仓库均由用户预先准备）。
func cloneAt(ctx context.Context, mirror, dst, base string) error {
	if res, err := runGit(ctx, "", "clone", "--quiet", "--no-hardlinks", mirror, dst); err != nil {
		return err
	} else if res.exitCode != 0 {
		return fmt.Errorf("git clone %s failed: %s", mirror, oneLine(res.stderr))
	}
	if res, err := runGit(ctx, dst, "checkout", "--quiet", base); err != nil {
		return err
	} else if res.exitCode != 0 {
		return fmt.Errorf("git checkout %s failed: %s", base, oneLine(res.stderr))
	}
	return nil
}

// oneLine 把多行文本压成单行（截断日志便于错误信息展示）。
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " ..."
	}
	return s
}
