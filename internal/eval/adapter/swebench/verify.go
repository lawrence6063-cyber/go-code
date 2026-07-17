// 本文件实现 swebench 的独立判定器（EVAL_SPEC §4.3/§5.2 接入模式 B）。
//
// 判定语义对齐 SWE-bench：把隐藏的 test_patch **瞬态**应用到工作区（agent 全程看不到），跑
// FAIL_TO_PASS + PASS_TO_PASS 指定的测试，退出码 0 视为通过；判完把 test_patch 反向还原，使工作区
// 对 agent 保持「无隐藏测试」的干净态。测试文件 pristine 由此天然成立——每次判定都以数据集的
// test_patch 为准覆盖，agent 改不动被实际执行的判定测试（verifier independence）。
package swebench

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alaindong/cogent/internal/sandbox"
	"github.com/alaindong/cogent/internal/verify"
)

// instanceVerifier 是一条 SWE-bench 样本的判定器：瞬态套隐藏测试、跑判定命令、还原。
type instanceVerifier struct {
	inst      Instance      // 样本（提供 base_commit / test_patch / FAIL_TO_PASS 等）
	patchFile string        // 写到工作区外的 test_patch 文件（agent 够不到）
	timeout   time.Duration // 判定命令超时
}

// Verify 见 verify.Verifier 接口说明：瞬态套隐藏测试 → 跑判定 → 还原。
// 任一 git/IO 步骤失败按 fail-closed 处理（Report.Passed=false 且返回 error）。
func (v instanceVerifier) Verify(ctx context.Context, workRoot, _ string) (verify.Report, error) {
	if strings.TrimSpace(v.inst.TestPatch) == "" {
		return verify.Report{Summary: "instance has no test_patch"}, fmt.Errorf("empty test_patch for %s", v.inst.InstanceID)
	}
	if err := v.applyTests(ctx, workRoot); err != nil {
		return verify.Report{Summary: "apply test_patch failed: " + err.Error()}, err
	}
	defer v.restore(workRoot)

	sb := sandbox.New(sandbox.Config{WorkRoot: workRoot, Enabled: false, Timeout: v.timeout})
	cmd := testCommand(v.inst, workRoot)
	res, err := sb.Exec(ctx, cmd)
	detail := strings.TrimSpace(strings.TrimSpace(res.Stdout) + "\n" + strings.TrimSpace(res.Stderr))
	if err != nil {
		return verify.Report{Summary: "test run failed: " + err.Error(), Detail: detail},
			fmt.Errorf("verify exec: %w", err)
	}
	if res.ExitCode == 0 {
		return verify.Report{Passed: true, Summary: "SWE-bench tests passed (exit 0)", Detail: detail}, nil
	}
	return verify.Report{
		Summary: fmt.Sprintf("SWE-bench tests not passed (exit %d)", res.ExitCode),
		Detail:  detail,
	}, nil
}

// applyTests 先把 test_patch 触及的文件还原到 base 提交（抹掉 agent 对判定测试的篡改），
// 再瞬态应用 test_patch（引入隐藏判定测试）。
func (v instanceVerifier) applyTests(ctx context.Context, workRoot string) error {
	for _, p := range patchPaths(v.inst.TestPatch) {
		// 已存在的测试文件恢复到 base；test_patch 新增的文件在 base 不存在，checkout 报错可忽略。
		_, _ = runGit(ctx, workRoot, "checkout", v.inst.BaseCommit, "--", p)
	}
	res, err := runGit(ctx, workRoot, "apply", "--whitespace=nowarn", v.patchFile)
	if err != nil {
		return err
	}
	if res.exitCode != 0 {
		return fmt.Errorf("git apply test_patch: %s", oneLine(res.stderr))
	}
	return nil
}

// restore 反向还原 test_patch，使工作区对 agent 保持无隐藏测试的干净态（尽力而为）。
func (v instanceVerifier) restore(workRoot string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = runGit(ctx, workRoot, "apply", "-R", "--whitespace=nowarn", v.patchFile)
	for _, p := range patchPaths(v.inst.TestPatch) {
		_, _ = runGit(ctx, workRoot, "checkout", v.inst.BaseCommit, "--", p)
	}
}

// testCommand 推导判定命令：显式 TestCmd 优先；否则按工作区特征推导——
// 有 go.mod 走 go test；有 Cargo.toml 走 cargo test；默认按 python pytest 跑 FAIL_TO_PASS+PASS_TO_PASS
// 指定的测试标识（SWE-bench 以 python 为主）。命令 cd 进工作区根、退出码即判据。
func testCommand(inst Instance, workRoot string) string {
	if c := strings.TrimSpace(inst.TestCmd); c != "" {
		return c
	}
	if fileExists(filepath.Join(workRoot, "go.mod")) {
		return "go test ./..."
	}
	if fileExists(filepath.Join(workRoot, "Cargo.toml")) {
		return "cargo test --quiet"
	}
	ids := append(append([]string{}, inst.FailToPass...), inst.PassToPass...)
	if len(ids) == 0 {
		return "python -m pytest -q"
	}
	return "python -m pytest -q " + strings.Join(quoteAll(ids), " ")
}

// quoteAll 给每个测试标识加单引号（pytest node id 可能含空格 / 特殊字符）。
func quoteAll(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, "'"+strings.ReplaceAll(id, "'", "")+"'")
	}
	return out
}

// patchPaths 从统一 diff 文本里抽出被触及的文件路径（取 "diff --git a/X b/Y" 的 b/Y）。
// 用于把 agent 对判定测试文件的改动还原到 base（假设路径不含空格，SWE-bench 满足）。
func patchPaths(patch string) []string {
	var paths []string
	seen := map[string]bool{}
	for _, line := range strings.Split(patch, "\n") {
		if !strings.HasPrefix(line, "diff --git ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		p := strings.TrimPrefix(fields[3], "b/")
		if p != "" && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	return paths
}

// fileExists 报告 path 是否为已存在的普通文件。
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
