package swebench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLocalizeHint_Disabled 覆盖默认关闭时不注入任何定位线索（保持基线行为）。
func TestLocalizeHint_Disabled(t *testing.T) {
	t.Setenv(localizeEnvVar, "")
	if got := localizeHint(t.TempDir(), Instance{ProblemStatement: "anything"}); got != "" {
		t.Errorf("localize disabled should return empty, got %q", got)
	}
}

// TestLocalizeHint_RanksRelevantFile 覆盖开启后：与 issue 高相关的源文件被排到线索前列，
// 且测试文件/vendor 目录被排除。
func TestLocalizeHint_RanksRelevantFile(t *testing.T) {
	t.Setenv(localizeEnvVar, "1")
	root := t.TempDir()
	mustWrite(t, root, "src/session.py",
		"class Session:\n    def resolve_redirects(self, resp):\n        # merge cookies across redirect\n        pass\n")
	mustWrite(t, root, "src/models.py", "class Response:\n    status_code = 200\n")
	mustWrite(t, root, "tests/test_session.py", "def test_resolve_redirects_cookie(): pass\n")
	mustWrite(t, root, "vendor/lib.py", "def cookie_redirect_session(): pass\n")

	hint := localizeHint(root, Instance{ProblemStatement: "Session.resolve_redirects drops cookie across redirect"})
	if !strings.Contains(hint, "src/session.py") {
		t.Errorf("hint should surface src/session.py, got:\n%s", hint)
	}
	if strings.Contains(hint, "tests/test_session.py") {
		t.Errorf("hint must not include test files:\n%s", hint)
	}
	if strings.Contains(hint, "vendor/lib.py") {
		t.Errorf("hint must not include vendored files:\n%s", hint)
	}
	if !strings.Contains(hint, "NOT exhaustive") {
		t.Errorf("hint should carry the non-exhaustive caveat")
	}
}

// TestLocalizeK 覆盖 top-k 环境覆盖。
func TestLocalizeK(t *testing.T) {
	t.Setenv(localizeKEnvVar, "3")
	if got := localizeK(); got != 3 {
		t.Errorf("localizeK() = %d, want 3", got)
	}
	t.Setenv(localizeKEnvVar, "bad")
	if got := localizeK(); got != defaultLocalizeK {
		t.Errorf("localizeK() bad value = %d, want default %d", got, defaultLocalizeK)
	}
}

func mustWrite(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
