package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alaindong/cogent/internal/loop"
)

func TestParseTaskYAML(t *testing.T) {
	src := `# comment line
id: feedback_convergence
difficulty: medium
languages: [go, python]
capabilities: [convergence]
workdir: repo
budget:                          # inline comment on block
  max_iterations: 8
  max_cost_usd: 2.0
  max_wallclock: 8m
expected_outcome: achieved
verifier: script                 # trailing comment
timeout: 5m
oracle: oracle/fix.patch
solvability_check: true
`
	y := parseTaskYAML([]byte(src))
	if y.ID != "feedback_convergence" || y.Difficulty != "medium" || y.Workdir != "repo" {
		t.Fatalf("scalar fields wrong: %+v", y)
	}
	if len(y.Languages) != 2 || y.Languages[0] != "go" || y.Languages[1] != "python" {
		t.Fatalf("languages wrong: %v", y.Languages)
	}
	if len(y.Capabilities) != 1 || y.Capabilities[0] != "convergence" {
		t.Fatalf("capabilities wrong: %v", y.Capabilities)
	}
	if y.Budget.MaxIterations != 8 || y.Budget.MaxCostUSD != 2.0 || y.Budget.MaxWallClock != "8m" {
		t.Fatalf("budget wrong: %+v", y.Budget)
	}
	if y.ExpectedOutcome != "achieved" || y.Verifier != "script" || y.Timeout != "5m" {
		t.Fatalf("outcome/verifier/timeout wrong: %+v", y)
	}
	if y.Oracle != "oracle/fix.patch" || !y.SolvabilityCheck {
		t.Fatalf("oracle/solvability wrong: %+v", y)
	}
}

// writeTask 在 root 下造一个任务目录（含 task.yaml/task.txt/verify.sh，可选 repo/ 与 oracle/）。
func writeTask(t *testing.T, root, name, yaml string, withRepo bool) string {
	t.Helper()
	dir := filepath.Join(root, name)
	mustMkdir(t, dir)
	mustWrite(t, filepath.Join(dir, "task.yaml"), yaml)
	mustWrite(t, filepath.Join(dir, "task.txt"), "fix "+name)
	mustWrite(t, filepath.Join(dir, "verify.sh"), "#!/usr/bin/env bash\nexit 1\n")
	if withRepo {
		mustMkdir(t, filepath.Join(dir, "repo"))
		mustWrite(t, filepath.Join(dir, "repo", "main.go"), "package main\n")
		mustMkdir(t, filepath.Join(dir, "oracle"))
		mustWrite(t, filepath.Join(dir, "oracle", "fix.patch"), "secret patch\n")
	}
	return dir
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixture 造一个含 3 个任务的 tasks 目录：easy/go/convergence(repo)、medium/python/budget(repo)、
// hard/go/exploration(repo-root，无 repo/)。
func fixture(t *testing.T) string {
	t.Helper()
	tasks := t.TempDir()
	writeTask(t, tasks, "t_easy", "id: t_easy\ndifficulty: easy\nlanguages: [go]\ncapabilities: [convergence]\nworkdir: repo\n", true)
	writeTask(t, tasks, "t_mid", "id: t_mid\ndifficulty: medium\nlanguages: [python]\ncapabilities: [budget]\nworkdir: repo\n", true)
	writeTask(t, tasks, "t_root", "id: t_root\ndifficulty: hard\nlanguages: [go]\ncapabilities: [exploration]\nworkdir: repo-root\n", false)
	return tasks
}

func TestLoadAll(t *testing.T) {
	specs, err := Load(fixture(t), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 3 {
		t.Fatalf("want 3 specs, got %d", len(specs))
	}
}

func TestLoadFilter(t *testing.T) {
	tasks := fixture(t)
	tests := []struct {
		name string
		f    Filter
		want int
	}{
		{"by-capability", Filter{Capabilities: []string{"budget"}}, 1},
		{"by-difficulty", Filter{Difficulties: []string{"easy"}}, 1},
		{"by-language-go", Filter{Languages: []string{"go"}}, 2},
		{"by-id", Filter{IDs: []string{"t_easy"}}, 1},
		{"cross-and-empty", Filter{Difficulties: []string{"easy"}, Capabilities: []string{"budget"}}, 0},
		{"no-match", Filter{Languages: []string{"rust"}}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			specs, err := Load(tasks, tc.f)
			if err != nil {
				t.Fatal(err)
			}
			if len(specs) != tc.want {
				t.Fatalf("want %d, got %d", tc.want, len(specs))
			}
		})
	}
}

func TestCasesWorkspaceCopy(t *testing.T) {
	tasks := fixture(t)
	ws := t.TempDir()
	a := Adapter{TasksDir: tasks, WorkspaceDir: ws}
	cases, err := a.Cases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// repo-root 任务被跳过，只剩 2 个自包含任务。
	if len(cases) != 2 {
		t.Fatalf("want 2 cases (repo-root skipped), got %d", len(cases))
	}
	var easy *struct{ workRoot, id string }
	for _, c := range cases {
		if c.ID == "native/t_easy" {
			easy = &struct{ workRoot, id string }{c.Goal.WorkRoot, c.ID}
		}
	}
	if easy == nil {
		t.Fatal("native/t_easy not found")
	}
	// 副本含 repo/ 与 verify.sh，但排除 oracle/（参考解不喂给 agent）。
	if !dirExists(filepath.Join(easy.workRoot, "repo")) {
		t.Error("workspace copy missing repo/")
	}
	if _, err := os.Stat(filepath.Join(easy.workRoot, "verify.sh")); err != nil {
		t.Error("workspace copy missing verify.sh")
	}
	if dirExists(filepath.Join(easy.workRoot, "oracle")) {
		t.Error("workspace copy must NOT contain oracle/ (gold patch leaked)")
	}
	// 源目录未被污染：oracle/ 仍在原地。
	if !dirExists(filepath.Join(tasks, "t_easy", "oracle")) {
		t.Error("source oracle/ was removed — source polluted")
	}
}

func TestCasesBudgetDefaults(t *testing.T) {
	tasks := t.TempDir()
	// 仅给 max_iterations，其余走 DefaultBudget 兜底。
	writeTask(t, tasks, "t_b", "id: t_b\ndifficulty: easy\nlanguages: [go]\ncapabilities: [budget]\nworkdir: repo\nbudget:\n  max_iterations: 3\nexpected_outcome: budget_spent\ntimeout: 2m\n", true)
	a := Adapter{TasksDir: tasks, WorkspaceDir: t.TempDir()}
	cases, err := a.Cases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 1 {
		t.Fatalf("want 1 case, got %d", len(cases))
	}
	c := cases[0]
	def := loop.DefaultBudget()
	if c.Goal.Budget.MaxIterations != 3 {
		t.Errorf("MaxIterations: want 3, got %d", c.Goal.Budget.MaxIterations)
	}
	if c.Goal.Budget.MaxCostUSD != def.MaxCostUSD || c.Goal.Budget.MaxWallClock != def.MaxWallClock {
		t.Errorf("unset budget fields should fall back to default: %+v", c.Goal.Budget)
	}
	if c.ExpectedOutcome != "budget_spent" {
		t.Errorf("ExpectedOutcome: want budget_spent, got %q", c.ExpectedOutcome)
	}
	if c.Timeout != 2*time.Minute {
		t.Errorf("Timeout: want 2m, got %v", c.Timeout)
	}
}

func TestPrepareMissingDir(t *testing.T) {
	a := Adapter{TasksDir: filepath.Join(t.TempDir(), "nope")}
	if err := a.Prepare(context.Background()); err == nil {
		t.Fatal("Prepare should fail on missing tasks dir")
	}
}

// TestPristineVerifierResistsTamper 断言：即便工作区的 verify.sh 被篡改为「恒通过」，
// 判定前也会被任务源目录的 pristine 脚本覆盖，篡改无效（verifier independence）。
func TestPristineVerifierResistsTamper(t *testing.T) {
	pdir := t.TempDir()
	wdir := t.TempDir()
	pristine := filepath.Join(pdir, "verify.sh")
	target := filepath.Join(wdir, "verify.sh")
	mustWrite(t, pristine, "#!/usr/bin/env bash\nexit 3\n") // pristine：恒失败
	mustWrite(t, target, "#!/usr/bin/env bash\nexit 0\n")   // 被 agent 篡改为恒通过

	v := Adapter{}.verifier(pristine, target)
	rep, err := v.Verify(context.Background(), wdir, "")
	if err != nil {
		t.Fatalf("verify err: %v", err)
	}
	if rep.Passed {
		t.Error("tampered target (exit 0) should be overwritten by pristine (exit 3) → not passed")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "#!/usr/bin/env bash\nexit 3\n" {
		t.Errorf("target not restored to pristine content, got: %q", got)
	}
}
