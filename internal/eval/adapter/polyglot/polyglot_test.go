package polyglot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alaindong/cogent/internal/verify"
)

// writeFile 在 path 写入内容（自动建父目录），失败即致命。
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeExercise 在 root 下造一门语言的一个练习（含 .docs/instructions.md、.meta/config.json+example、
// 解题 stub 与测试文件），返回练习目录。
func writeExercise(t *testing.T, root, lang, slug, solFile, testFile string) string {
	t.Helper()
	dir := filepath.Join(root, lang, practiceRel, slug)
	writeFile(t, filepath.Join(dir, ".docs", "instructions.md"), "Implement "+slug+".")
	cfg := `{"files":{"solution":["` + solFile + `"],"test":["` + testFile + `"],"example":[".meta/example.` + lang + `"]}}`
	writeFile(t, filepath.Join(dir, ".meta", "config.json"), cfg)
	writeFile(t, filepath.Join(dir, ".meta", "example."+lang), "REFERENCE SOLUTION — must not leak\n")
	writeFile(t, filepath.Join(dir, solFile), "// TODO: implement\n")
	writeFile(t, filepath.Join(dir, testFile), "PRISTINE TEST CONTENT\n")
	return dir
}

// fixture 造一个含 go/two-fer 与 python/leap 两个练习的 polyglot 数据集根。
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeExercise(t, root, "go", "two-fer", "two_fer.go", "two_fer_test.go")
	writeExercise(t, root, "python", "leap", "leap.py", "leap_test.py")
	return root
}

func TestLoadAllAndFilter(t *testing.T) {
	root := fixture(t)
	tests := []struct {
		name string
		f    Filter
		want int
	}{
		{"all", Filter{}, 2},
		{"lang-go", Filter{Languages: []string{"go"}}, 1},
		{"lang-unsupported", Filter{Languages: []string{"cobol"}}, 0},
		{"exercise", Filter{Exercises: []string{"leap"}}, 1},
		{"limit-per-lang", Filter{Limit: 1}, 2}, // 每语言各 1 个练习，两语言共 2
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			specs, err := Load(root, tc.f)
			if err != nil {
				t.Fatal(err)
			}
			if len(specs) != tc.want {
				t.Fatalf("want %d specs, got %d", tc.want, len(specs))
			}
		})
	}
}

func TestLoadDeterministicOrder(t *testing.T) {
	specs, err := Load(fixture(t), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	// 语言字典序：go 在 python 之前。
	if specs[0].Language != "go" || specs[1].Language != "python" {
		t.Fatalf("specs not in deterministic language order: %+v", specs)
	}
}

func TestCasesWorkspaceCopyExcludesMeta(t *testing.T) {
	root := fixture(t)
	ws := t.TempDir()
	a := Adapter{Root: root, WorkspaceDir: ws, Filter: Filter{Languages: []string{"go"}}}
	cases, err := a.Cases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 1 {
		t.Fatalf("want 1 case, got %d", len(cases))
	}
	c := cases[0]
	if c.ID != "polyglot/go/two-fer" {
		t.Errorf("unexpected case id: %s", c.ID)
	}
	wr := c.Goal.WorkRoot
	// 副本含解题文件与测试文件。
	for _, f := range []string{"two_fer.go", "two_fer_test.go"} {
		if !fileExists(filepath.Join(wr, f)) {
			t.Errorf("workspace copy missing %s", f)
		}
	}
	// 副本必须排除 .meta/（参考解不泄露）。
	if dirExists(filepath.Join(wr, ".meta")) {
		t.Error("workspace copy must NOT contain .meta/ (reference solution leaked)")
	}
	// 源目录未被污染：.meta/ 仍在原地。
	if !dirExists(filepath.Join(root, "go", practiceRel, "two-fer", ".meta")) {
		t.Error("source .meta/ removed — dataset polluted")
	}
	// 元数据正确。
	if len(c.Meta.Languages) != 1 || c.Meta.Languages[0] != "go" || c.Meta.Source != "polyglot" {
		t.Errorf("unexpected meta: %+v", c.Meta)
	}
	if c.ExpectedOutcome != "achieved" {
		t.Errorf("expected outcome achieved, got %s", c.ExpectedOutcome)
	}
}

func TestCasesIntentAndVerifyScript(t *testing.T) {
	root := fixture(t)
	ws := t.TempDir()
	a := Adapter{Root: root, WorkspaceDir: ws, Filter: Filter{Languages: []string{"go"}}}
	cases, err := a.Cases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c := cases[0]
	wr := c.Goal.WorkRoot
	// 工作区目录命名为 slug（cpp CMake 依赖），位于 case 目录下。
	caseDir := filepath.Join(ws, "go_two-fer")
	if wr != filepath.Join(caseDir, "two-fer") {
		t.Errorf("workRoot should be <caseDir>/<slug>, got %s", wr)
	}
	// intent 含题面与解题文件名，并指明测试命令。
	if !strings.Contains(c.Goal.Intent, "Implement two-fer.") {
		t.Errorf("intent missing instructions: %q", c.Goal.Intent)
	}
	if !strings.Contains(c.Goal.Intent, "two_fer.go") || !strings.Contains(c.Goal.Intent, "go test ./...") {
		t.Errorf("intent missing solution file or test cmd: %q", c.Goal.Intent)
	}
	// 判定脚本生成在工作区外（case 目录下、workRoot 之外），agent 够不到。
	script := filepath.Join(caseDir, "verify.sh")
	if !fileExists(script) {
		t.Fatal("verify.sh not generated at case dir (outside workspace)")
	}
	if strings.HasPrefix(script, wr) {
		t.Error("verify.sh must live outside the agent workRoot")
	}
	data, _ := os.ReadFile(script)
	if !strings.Contains(string(data), "go test ./...") {
		t.Errorf("verify.sh missing test command: %q", data)
	}
}

// recordingVerifier 是注入 testVerifier 的替身，只记录被调用，避免真实工具链执行。
type recordingVerifier struct{ called bool }

func (r *recordingVerifier) Verify(_ context.Context, _, _ string) (verify.Report, error) {
	r.called = true
	return verify.Report{Passed: true}, nil
}

func TestTestVerifierRestoresPristine(t *testing.T) {
	src := t.TempDir()
	ws := t.TempDir()
	pristine := filepath.Join(src, "leap_test.py")
	target := filepath.Join(ws, "leap_test.py")
	writeFile(t, pristine, "PRISTINE TEST\n")
	writeFile(t, target, "TAMPERED BY AGENT\n") // agent 篡改工作区测试文件

	rec := &recordingVerifier{}
	v := testVerifier{restore: []restorePair{{src: pristine, dst: target}}, inner: rec}
	rep, err := v.Verify(context.Background(), ws, "")
	if err != nil {
		t.Fatalf("verify err: %v", err)
	}
	if !rec.called {
		t.Error("inner verifier should be invoked after restore")
	}
	if !rep.Passed {
		t.Error("expected inner report passed=true")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "PRISTINE TEST\n" {
		t.Errorf("test file not restored to pristine, got: %q", got)
	}
}

func TestPrepareErrors(t *testing.T) {
	// 根不存在。
	if err := (Adapter{Root: filepath.Join(t.TempDir(), "nope")}).Prepare(context.Background()); err == nil {
		t.Error("Prepare should fail on missing root")
	}
	// 根存在但无任何受支持语言 track。
	empty := t.TempDir()
	if err := (Adapter{Root: empty}).Prepare(context.Background()); err == nil {
		t.Error("Prepare should fail when no supported language track present")
	}
	// 正常根应通过。
	if err := (Adapter{Root: fixture(t)}).Prepare(context.Background()); err != nil {
		t.Errorf("Prepare on valid root should succeed: %v", err)
	}
}
