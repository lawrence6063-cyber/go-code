package terminalbench

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/alaindong/cogent/internal/eval/adapter"
)

// fixture 任务源码（自包含、Docker-free）：目标是创建 greeting.txt。
const (
	fixtureTaskYAML = "instruction: |\n" +
		"  Create a file named greeting.txt in the current directory whose\n" +
		"  contents are exactly the single line: hello from cogent\n" +
		"difficulty: easy\n" +
		"tags: [file-manipulation]\n"
	fixtureSolution = "#!/usr/bin/env bash\nset -euo pipefail\necho \"hello from cogent\" > greeting.txt\n"
	fixtureRunTests = "#!/usr/bin/env bash\nset -euo pipefail\nbash tests/test_outputs.sh\n"
	fixtureTest     = "#!/usr/bin/env bash\nset -euo pipefail\n[ -f greeting.txt ] || { echo 'missing greeting.txt'; exit 1; }\n" +
		"got=\"$(cat greeting.txt)\"\n[ \"$got\" = \"hello from cogent\" ] || { echo \"wrong: $got\"; exit 1; }\necho PASS\n"
	fixtureStarter = "# scratch file that agents may see (not a test)\n"
)

// TestAdapterOracleFixture 是 terminalbench Adapter 的 fixture 可解性自证（EVAL_SPEC §5.1 的
// Terminal-Bench 版，对标 polyglot/swebench 的 oracle 测试）：用一个自包含、Docker-free 的合成任务
// （指令 + 隐藏测试 run-tests.sh/tests/ + oracle solution.sh），走**真实 Adapter + taskVerifier
// 代码路径**，在无 Docker / 无网络下验证：
//   - 数据集扫描与 task.yaml 解析、工作区隔离（排除隐藏测试与参考解）；
//   - 隐藏测试瞬态注入后跑 run-tests.sh——修复前失败、oracle solution 应用后通过；
//   - 判定后注入的测试资产被移除（工作区对 agent 保持无隐藏测试的干净态）。
func TestAdapterOracleFixture(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH; skipping terminalbench fixture oracle test")
	}
	dataset := buildFixtureDataset(t)

	ctx := context.Background()
	a := Adapter{DatasetDir: dataset, WorkspaceDir: t.TempDir(), VerifyTimeout: 2 * time.Minute}
	if err := a.Prepare(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	cases, err := a.Cases(ctx)
	if err != nil {
		t.Fatalf("cases: %v", err)
	}
	if len(cases) != 1 {
		t.Fatalf("want 1 case, got %d", len(cases))
	}
	c := cases[0]
	if c.ID != "terminalbench/create-greeting" || c.Meta.Source != "terminalbench" {
		t.Errorf("case meta wrong: id=%s meta=%+v", c.ID, c.Meta)
	}
	assertWorkspaceIsolated(t, c.Goal.WorkRoot)
	assertVerifyBeforeAfter(t, ctx, c, a.datasetTaskDir())
}

// assertWorkspaceIsolated 断言工作区排除隐藏测试与参考解，但保留普通起始文件。
func assertWorkspaceIsolated(t *testing.T, workRoot string) {
	t.Helper()
	for _, hidden := range []string{"run-tests.sh", "tests", "solution.sh", "task.yaml"} {
		if _, err := os.Stat(filepath.Join(workRoot, hidden)); err == nil {
			t.Errorf("workspace should NOT contain hidden asset %q", hidden)
		}
	}
	if _, err := os.Stat(filepath.Join(workRoot, "starter.txt")); err != nil {
		t.Errorf("workspace should contain non-test starter file: %v", err)
	}
}

// assertVerifyBeforeAfter 断言：修复前判定失败、注入测试判完移除、oracle solution 应用后判定通过。
func assertVerifyBeforeAfter(t *testing.T, ctx context.Context, c adapter.Case, taskDir string) {
	t.Helper()
	workRoot := c.Goal.WorkRoot
	rep, err := c.Goal.Verifier.Verify(ctx, workRoot, c.Goal.Intent)
	if err != nil {
		t.Fatalf("verify before fix errored: %v\n%s", err, rep.Detail)
	}
	if rep.Passed {
		t.Fatalf("verify before fix should FAIL, but passed: %s", rep.Summary)
	}
	for _, injected := range []string{"run-tests.sh", "tests"} {
		if _, err := os.Stat(filepath.Join(workRoot, injected)); err == nil {
			t.Errorf("injected test asset %q should be removed after verify", injected)
		}
	}

	// 应用 oracle 参考解（模拟 agent 交出正确答案）。
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(taskDir, "solution.sh"))
	cmd.Dir = workRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("apply oracle solution: %v\n%s", err, out)
	}
	rep2, err := c.Goal.Verifier.Verify(ctx, workRoot, c.Goal.Intent)
	if err != nil {
		t.Fatalf("verify after fix errored: %v\n%s", err, rep2.Detail)
	}
	if !rep2.Passed {
		t.Fatalf("verify after oracle should PASS: %s\n%s", rep2.Summary, rep2.Detail)
	}
}

// datasetTaskDir 返回 fixture 数据集里唯一任务的源目录（供测试读取 solution.sh）。
func (a Adapter) datasetTaskDir() string {
	return filepath.Join(a.DatasetDir, "tasks", "create-greeting")
}

// buildFixtureDataset 在临时目录搭出一个 Terminal-Bench 格式的 Docker-free 任务，返回数据集根。
func buildFixtureDataset(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	taskDir := filepath.Join(root, "tasks", "create-greeting")
	writeFile(t, filepath.Join(taskDir, "task.yaml"), fixtureTaskYAML)
	writeFile(t, filepath.Join(taskDir, "solution.sh"), fixtureSolution)
	writeFile(t, filepath.Join(taskDir, "run-tests.sh"), fixtureRunTests)
	writeFile(t, filepath.Join(taskDir, "tests", "test_outputs.sh"), fixtureTest)
	writeFile(t, filepath.Join(taskDir, "starter.txt"), fixtureStarter)
	return root
}

// writeFile 写文件（含父目录，失败即 fatal）。
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
