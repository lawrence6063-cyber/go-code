package swebench

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alaindong/cogent/internal/eval/adapter"
)

// fixtureCase 描述一个自包含合成仓库的可解性自证场景（跨语言复用同一套 harness 断言）。
type fixtureCase struct {
	name        string            // 子测试名
	repoName    string            // 镜像目录名（<repos>/<repoName>）
	repoSlug    string            // instance.repo（如 "acme/widget"）
	baseFiles   map[string]string // base 提交的文件（含缺陷）
	fixFile     string            // 模拟 agent 修复时覆盖的文件
	fixContent  string            // 修复后的内容
	testFile    string            // 隐藏测试文件（由 test_patch 引入）
	testContent string            // 隐藏测试文件内容
	failToPass  []string          // FAIL_TO_PASS 测试标识
	testCmd     string            // 显式判定命令（""=按语言推导）
	bins        []string          // 该场景所需二进制（缺失即跳过）
	patchFile   string            // model_patch 应包含的源码文件名
}

// 固定的 Go / Python fixture 源码：函数故意写成减法（缺陷），修复为加法。
const (
	buggySum = "package widget\n\n// Sum 返回 a 与 b 的和。\nfunc Sum(a, b int) int {\n\treturn a - b\n}\n"
	fixedSum = "package widget\n\n// Sum 返回 a 与 b 的和。\nfunc Sum(a, b int) int {\n\treturn a + b\n}\n"
	goMod    = "module widget\n\ngo 1.21\n"
	sumTest  = "package widget\n\nimport \"testing\"\n\nfunc TestSum(t *testing.T) {\n\tif got := Sum(1, 2); got != 3 {\n\t\tt.Fatalf(\"Sum(1,2) = %d, want 3\", got)\n\t}\n}\n"

	buggyCalc = "def add(a, b):\n    return a - b\n"
	fixedCalc = "def add(a, b):\n    return a + b\n"
	calcTest  = "from calc import add\n\n\ndef test_add():\n    assert add(1, 2) == 3\n"
)

// fixtureCases 是跨语言的 harness 自证场景：Go（go.mod 自动推导 go test）与
// Python（显式 python3 -m pytest，SWE-bench 主语言路径）。
func fixtureCases() []fixtureCase {
	return []fixtureCase{
		{
			name: "go", repoName: "acme__widget", repoSlug: "acme/widget",
			baseFiles: map[string]string{"go.mod": goMod, "sum.go": buggySum},
			fixFile:   "sum.go", fixContent: fixedSum,
			testFile: "sum_test.go", testContent: sumTest,
			failToPass: []string{"TestSum"}, bins: []string{"git", "go"}, patchFile: "sum.go",
		},
		{
			name: "python", repoName: "acme__calc", repoSlug: "acme/calc",
			baseFiles: map[string]string{"calc.py": buggyCalc},
			fixFile:   "calc.py", fixContent: fixedCalc,
			testFile: "test_calc.py", testContent: calcTest,
			failToPass: []string{"test_calc.py::test_add"},
			testCmd:    "python3 -m pytest -q test_calc.py::test_add",
			bins:       []string{"git", "python3"}, patchFile: "calc.py",
		},
	}
}

// TestAdapterOracleFixture 是 swebench Adapter 的 fixture 可解性自证（EVAL_SPEC §5.1 的 swebench 版，
// 对标 polyglot 的 TestOracleSolutionsPass）：用自包含的合成 git 仓库（缺陷源码 + 隐藏 test_patch +
// gold 参考解），走**真实 Adapter + instanceVerifier 代码路径**，在无 Docker / 无网络下验证：
//   - 数据集加载与仓库镜像解析、工作区隔离 clone/checkout；
//   - 隐藏 test_patch 瞬态应用后跑测试——修复前 FAIL_TO_PASS 失败、gold 修复后通过；
//   - 判定后 test_patch 被还原（工作区对 agent 保持无隐藏测试的干净态）；
//   - Mode A predictions 抽取的 model_patch 含源码改动、排除测试文件。
//
// Python 变体额外覆盖 SWE-bench 主语言的 pytest 判定路径（本机 python 命令为 python3）。
func TestAdapterOracleFixture(t *testing.T) {
	if _, err := exec.LookPath("python3"); err == nil {
		requirePytest(t)
	}
	for _, fc := range fixtureCases() {
		t.Run(fc.name, func(t *testing.T) {
			requireBins(t, fc.bins...)
			runFixtureOracle(t, fc)
		})
	}
}

// runFixtureOracle 对单个场景走完整 harness 自证流程。
func runFixtureOracle(t *testing.T, fc fixtureCase) {
	reposDir := t.TempDir()
	mirror := filepath.Join(reposDir, fc.repoName)
	base, testPatch := buildRepo(t, mirror, fc)

	dataset := filepath.Join(t.TempDir(), "instances.jsonl")
	writeDataset(t, dataset, Instance{
		InstanceID:       fc.repoName + "-1",
		Repo:             fc.repoSlug,
		BaseCommit:       base,
		ProblemStatement: "function returns the difference instead of the sum",
		TestPatch:        testPatch,
		FailToPass:       stringList(fc.failToPass),
		TestCmd:          fc.testCmd,
	})

	ctx := context.Background()
	a := Adapter{DatasetFile: dataset, ReposDir: reposDir, WorkspaceDir: t.TempDir(), VerifyTimeout: 3 * time.Minute}
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
	if c.ID != "swebench/"+fc.repoName+"-1" || c.Meta.Source != "swebench" || c.Meta.Difficulty != "hard" {
		t.Errorf("case meta wrong: id=%s meta=%+v", c.ID, c.Meta)
	}
	workRoot := c.Goal.WorkRoot
	assertWorkspaceIsolated(t, workRoot, fc)
	assertVerifyBeforeAfter(t, ctx, c, workRoot, fc)
	assertPrediction(t, ctx, dataset, workRoot, fc.patchFile, fc.testFile)
}

// assertVerifyBeforeAfter 断言：修复前判定失败、test_patch 判完还原、gold 修复后判定通过。
func assertVerifyBeforeAfter(t *testing.T, ctx context.Context, c adapter.Case, workRoot string, fc fixtureCase) {
	t.Helper()
	rep, err := c.Goal.Verifier.Verify(ctx, workRoot, c.Goal.Intent)
	if err != nil {
		t.Fatalf("verify before fix errored: %v\n%s", err, rep.Detail)
	}
	if rep.Passed {
		t.Fatalf("verify before fix should FAIL, but passed: %s", rep.Summary)
	}
	if fileExists(filepath.Join(workRoot, fc.testFile)) {
		t.Errorf("test_patch should be restored after verify; %s still present", fc.testFile)
	}
	if err := os.WriteFile(filepath.Join(workRoot, fc.fixFile), []byte(fc.fixContent), 0o644); err != nil {
		t.Fatalf("apply fix: %v", err)
	}
	rep2, err := c.Goal.Verifier.Verify(ctx, workRoot, c.Goal.Intent)
	if err != nil {
		t.Fatalf("verify after fix errored: %v\n%s", err, rep2.Detail)
	}
	if !rep2.Passed {
		t.Fatalf("verify after fix should PASS: %s\n%s", rep2.Summary, rep2.Detail)
	}
}

// assertWorkspaceIsolated 断言工作区是隔离副本：含缺陷源码、无隐藏测试。
func assertWorkspaceIsolated(t *testing.T, workRoot string, fc fixtureCase) {
	t.Helper()
	if !fileExists(filepath.Join(workRoot, fc.fixFile)) {
		t.Fatalf("workspace missing %s", fc.fixFile)
	}
	if fileExists(filepath.Join(workRoot, fc.testFile)) {
		t.Fatalf("workspace should not contain hidden test %s before verify", fc.testFile)
	}
}

// assertPrediction 验证 Mode A predictions 抽取：model_patch 含源码改动、排除测试文件，且可序列化为 JSONL。
func assertPrediction(t *testing.T, ctx context.Context, dataset, workRoot, srcFile, testFile string) {
	t.Helper()
	insts, err := LoadInstances(dataset, Filter{})
	if err != nil || len(insts) != 1 {
		t.Fatalf("reload instances: %v (n=%d)", err, len(insts))
	}
	p, err := CollectPrediction(ctx, insts[0], workRoot, "test-model")
	if err != nil {
		t.Fatalf("collect prediction: %v", err)
	}
	if p.ModelNameOrPath != "test-model" || p.InstanceID != insts[0].InstanceID {
		t.Errorf("prediction meta wrong: %+v", p)
	}
	if !strings.Contains(p.ModelPatch, srcFile) || strings.Contains(p.ModelPatch, testFile) {
		t.Errorf("model_patch should contain %s and exclude %s:\n%s", srcFile, testFile, p.ModelPatch)
	}
	var buf bytes.Buffer
	if err := WritePredictions(&buf, []Prediction{p}); err != nil {
		t.Fatalf("write predictions: %v", err)
	}
	var decoded Prediction
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &decoded); err != nil {
		t.Fatalf("predictions jsonl not valid: %v", err)
	}
	if decoded.InstanceID != p.InstanceID {
		t.Errorf("round-trip instance_id mismatch: %q", decoded.InstanceID)
	}
}

// TestOracleGoldPatchPasses 是受 SWEBENCH_FILE + SWEBENCH_REPOS 门控的真实数据集可解性自证
// （对标 polyglot 的 TestOracleSolutionsPass）：对数据集中若干样本，应用官方 gold patch 代替 agent
// 产出，走真实 adapter+verifier 判定，断言 FAIL_TO_PASS/PASS_TO_PASS 通过——不花 LLM 钱证明真实数据下
// harness 正确。需用户预先准备数据集 JSONL 与本地仓库镜像；真实 Python 仓库测试多需其依赖环境（Docker）。
// 每语言/总取样数由 SWEBENCH_ORACLE_LIMIT 控制（默认 1）。
func TestOracleGoldPatchPasses(t *testing.T) {
	file, repos := os.Getenv("SWEBENCH_FILE"), os.Getenv("SWEBENCH_REPOS")
	if file == "" || repos == "" {
		t.Skip("SWEBENCH_FILE / SWEBENCH_REPOS not set; skipping swebench real-dataset oracle test")
	}
	requireBins(t, "git")
	limit := 1
	if v := os.Getenv("SWEBENCH_ORACLE_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	insts, err := LoadInstances(file, Filter{Limit: limit})
	if err != nil {
		t.Fatalf("load dataset: %v", err)
	}
	ctx := context.Background()
	a := Adapter{DatasetFile: file, ReposDir: repos, WorkspaceDir: t.TempDir(), VerifyTimeout: 20 * time.Minute}
	for _, inst := range insts {
		runGoldOracle(t, ctx, a, inst)
	}
}

// runGoldOracle 为一条真实样本 clone 工作区、应用 gold patch、跑判定并断言通过。
func runGoldOracle(t *testing.T, ctx context.Context, a Adapter, inst Instance) {
	t.Helper()
	t.Run(inst.InstanceID, func(t *testing.T) {
		if strings.TrimSpace(inst.Patch) == "" {
			t.Skipf("instance %s has no gold patch", inst.InstanceID)
		}
		cases, err := (Adapter{DatasetFile: a.DatasetFile, ReposDir: a.ReposDir, WorkspaceDir: t.TempDir(),
			Filter: Filter{InstanceIDs: []string{inst.InstanceID}}, VerifyTimeout: a.VerifyTimeout}).Cases(ctx)
		if err != nil || len(cases) != 1 {
			t.Fatalf("build case for %s: err=%v n=%d", inst.InstanceID, err, len(cases))
		}
		c := cases[0]
		gold := filepath.Join(t.TempDir(), "gold.patch")
		if err := os.WriteFile(gold, []byte(inst.Patch), 0o644); err != nil {
			t.Fatal(err)
		}
		if res, err := runGit(ctx, c.Goal.WorkRoot, "apply", "--whitespace=nowarn", gold); err != nil || res.exitCode != 0 {
			t.Fatalf("apply gold patch: err=%v exit=%d %s", err, res.exitCode, res.stderr)
		}
		rep, err := c.Goal.Verifier.Verify(ctx, c.Goal.WorkRoot, c.Goal.Intent)
		if err != nil {
			t.Fatalf("verify %s errored: %v\n%s", inst.InstanceID, err, tail(rep.Detail, 1500))
		}
		if !rep.Passed {
			t.Errorf("gold patch should PASS for %s: %s\n%s", inst.InstanceID, rep.Summary, tail(rep.Detail, 1500))
		}
	})
}

// buildRepo 初始化合成镜像仓库（缺陷源码 + 一次提交为 base），并生成隐藏 test_patch 后
// 把仓库恢复到干净的 base 态。返回 (base 提交, test_patch diff 文本)。
func buildRepo(t *testing.T, mirror string, fc fixtureCase) (string, string) {
	t.Helper()
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, mirror, "init", "--quiet")
	for name, content := range fc.baseFiles {
		writeFile(t, filepath.Join(mirror, name), content)
	}
	mustGit(t, mirror, "add", ".")
	mustGit(t, mirror, "-c", "user.email=eval@test", "-c", "user.name=eval", "commit", "--quiet", "-m", "base")
	base := strings.TrimSpace(gitOut(t, mirror, "rev-parse", "HEAD"))

	writeFile(t, filepath.Join(mirror, fc.testFile), fc.testContent)
	mustGit(t, mirror, "add", fc.testFile)
	testPatch := gitOut(t, mirror, "diff", "--cached", "--", fc.testFile)
	mustGit(t, mirror, "reset", "--quiet", "--", fc.testFile)
	if err := os.Remove(filepath.Join(mirror, fc.testFile)); err != nil {
		t.Fatal(err)
	}
	if testPatch == "" {
		t.Fatal("generated empty test_patch")
	}
	return base, testPatch
}

// writeDataset 把一条 instance 序列化为单行 JSONL 写入 path。
func writeDataset(t *testing.T, path string, inst Instance) {
	t.Helper()
	data, err := json.Marshal(inst)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data)+"\n")
}

// requireBins 缺任一二进制则跳过测试（无工具链环境不算失败）。
func requireBins(t *testing.T, bins ...string) {
	t.Helper()
	for _, b := range bins {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%q not on PATH; skipping", b)
		}
	}
}

// requirePytest 缺 pytest 模块时跳过（Python 场景依赖它）。
func requirePytest(t *testing.T) {
	t.Helper()
	if err := exec.Command("python3", "-c", "import pytest").Run(); err != nil {
		t.Skip("python3 pytest module not available; skipping python fixture")
	}
}

// mustGit 在 dir 执行 git，失败或非 0 退出即 fatal。
func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	res, err := runGit(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	if res.exitCode != 0 {
		t.Fatalf("git %s exit %d: %s", strings.Join(args, " "), res.exitCode, res.stderr)
	}
}

// gitOut 在 dir 执行 git 并返回 stdout（失败即 fatal）。
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	res, err := runGit(context.Background(), dir, args...)
	if err != nil || res.exitCode != 0 {
		t.Fatalf("git %s: err=%v exit=%d %s", strings.Join(args, " "), err, res.exitCode, res.stderr)
	}
	return res.stdout
}

// writeFile 写文件（失败即 fatal）。
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// tail 返回字符串末尾至多 n 字节（截断长日志便于阅读）。
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "...(truncated)...\n" + s[len(s)-n:]
}
