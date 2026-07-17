// 本文件实现 Terminal-Bench 的独立判定器（EVAL_SPEC §4.3/§5.2 接入模式 B）与 Adapter 所需的
// 文件系统 / 筛选辅助函数。
//
// 判定语义：把隐藏的测试资产（run-tests.sh + tests/）**瞬态**注入工作区（agent 全程看不到），
// 执行 run-tests.sh（退出码 0 视为通过），判完把注入的资产移除，使工作区对 agent 保持无隐藏测试的
// 干净态。测试文件 pristine 由此天然成立——每次判定都以数据集源为准注入，agent 改不动被实际执行的测试。
package terminalbench

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alaindong/cogent/internal/sandbox"
	"github.com/alaindong/cogent/internal/verify"
)

// taskVerifier 是一个 Terminal-Bench 任务的判定器：瞬态注入隐藏测试、跑 run-tests.sh、移除。
type taskVerifier struct {
	srcDir    string        // 任务源目录（提供隐藏的 run-tests.sh + tests/，agent sandbox 够不到）
	runScript string        // 工作区内执行的测试入口脚本名（run-tests.sh）
	timeout   time.Duration // 判定命令超时
}

// injectedNames 是判定时瞬态注入、判完移除的顶层名字（隐藏测试资产）。
var injectedNames = []string{runTestsScript, "tests"}

// Verify 见 verify.Verifier 接口说明：注入隐藏测试 → 跑 run-tests.sh → 移除。
// 任一注入/执行步骤失败按 fail-closed 处理（Report.Passed=false 且返回 error）。
func (v taskVerifier) Verify(ctx context.Context, workRoot, _ string) (verify.Report, error) {
	if err := v.inject(workRoot); err != nil {
		return verify.Report{Summary: "inject hidden tests failed: " + err.Error()}, err
	}
	defer v.cleanup(workRoot)

	sb := sandbox.New(sandbox.Config{WorkRoot: workRoot, Enabled: false, Timeout: v.timeout})
	res, err := sb.Exec(ctx, "bash "+v.runScript)
	detail := strings.TrimSpace(strings.TrimSpace(res.Stdout) + "\n" + strings.TrimSpace(res.Stderr))
	if err != nil {
		return verify.Report{Summary: "test script failed to run: " + err.Error(), Detail: detail},
			fmt.Errorf("verify exec: %w", err)
	}
	if res.ExitCode == 0 {
		return verify.Report{Passed: true, Summary: "terminal-bench tests passed (exit 0)", Detail: detail}, nil
	}
	return verify.Report{
		Summary: fmt.Sprintf("terminal-bench tests not passed (exit %d)", res.ExitCode),
		Detail:  detail,
	}, nil
}

// inject 把源目录的隐藏测试资产复制进工作区（run-tests.sh 文件、tests/ 目录）。
func (v taskVerifier) inject(workRoot string) error {
	for _, name := range injectedNames {
		src := filepath.Join(v.srcDir, name)
		info, err := os.Stat(src)
		if err != nil {
			continue // 该资产不存在（如无 tests/ 目录）则跳过
		}
		dst := filepath.Join(workRoot, name)
		if info.IsDir() {
			if err := copyTree(src, dst, nil); err != nil {
				return err
			}
		} else if err := copyFile(src, dst); err != nil {
			return err
		}
	}
	return nil
}

// cleanup 移除判定时注入的隐藏测试资产，使工作区对 agent 保持干净态（尽力而为）。
func (v taskVerifier) cleanup(workRoot string) {
	for _, name := range injectedNames {
		_ = os.RemoveAll(filepath.Join(workRoot, name))
	}
}

// anyEqual 报告 want 为空（不限）或 have 命中 want 中任一项（大小写不敏感）。
func anyEqual(want []string, have string) bool {
	if len(want) == 0 {
		return true
	}
	return contains(want, have)
}

// contains 报告 have 是否（大小写不敏感）命中 want 任一项。
func contains(want []string, have string) bool {
	for _, w := range want {
		if strings.EqualFold(strings.TrimSpace(w), have) {
			return true
		}
	}
	return false
}

// anyIntersect 报告 want 为空（不限）或 want 与 have 有交集（大小写不敏感）。
func anyIntersect(want, have []string) bool {
	if len(want) == 0 {
		return true
	}
	for _, h := range have {
		if contains(want, h) {
			return true
		}
	}
	return false
}

// sanitize 把 task id 里的路径分隔符换成下划线，作为安全的目录名。
func sanitize(id string) string {
	return strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(id)
}

// dirExists 报告 path 是否为已存在的目录。
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// fileExists 报告 path 是否为已存在的普通文件。
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// copyTree 递归复制 src 到 dst，跳过 src 顶层名字命中 skipTop 的项。目标已存在则先清空。
func copyTree(src, dst string, skipTop map[string]bool) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if skipTop[e.Name()] {
			continue
		}
		if err := copyEntry(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()), e); err != nil {
			return err
		}
	}
	return nil
}

// copyEntry 复制单个目录项（目录递归、普通文件按内容+权限复制，符号链接等忽略）。
func copyEntry(src, dst string, e os.DirEntry) error {
	if e.IsDir() {
		return copyTree(src, dst, nil)
	}
	if !e.Type().IsRegular() {
		return nil
	}
	return copyFile(src, dst)
}

// copyFile 按内容与权限复制单个普通文件，必要时创建父目录。
func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
