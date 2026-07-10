// Package tool 中的 diagnostics_test.go 覆盖 diagnostics 工具的路径校验、命令构造与执行汇总。
package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/sandbox"
)

func TestValidateDiagnosticsPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"empty defaults to ellipsis", "", false},
		{"explicit ellipsis", "./...", false},
		{"sub package ellipsis", "sub/...", false},
		{"plain dir", "sub", false},
		{"disallowed semicolon", "sub; rm -rf /", true},
		{"disallowed space", "sub dir", true},
		{"disallowed dollar", "$(whoami)", true},
		{"escape outside workroot", "../../etc", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateDiagnosticsPath(dir, tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateDiagnosticsPath(%q) err = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestBuildDiagnosticsSections(t *testing.T) {
	sections := buildDiagnosticsSections("./...")
	if len(sections) != 3 {
		t.Fatalf("got %d sections, want 3", len(sections))
	}
	if !strings.Contains(sections[0].cmd, "xargs -r gofmt -l") {
		t.Errorf("default pattern gofmt command = %q, want find+xargs form", sections[0].cmd)
	}
	if !strings.Contains(sections[1].cmd, "go vet './...'") {
		t.Errorf("go vet command = %q", sections[1].cmd)
	}

	sections = buildDiagnosticsSections("internal/tool")
	if !strings.Contains(sections[0].cmd, "gofmt -l 'internal/tool'") {
		t.Errorf("explicit path gofmt command = %q, want direct gofmt -l form", sections[0].cmd)
	}
}

// TestDiagnosticsTool_RunsOnRealGoModule 端到端跑一次真实的 diagnostics 工具，
// 针对项目自身模块的 internal/tool 包，验证三个 section 都产出且格式正确、无 exec 报错。
func TestDiagnosticsTool_RunsOnRealGoModule(t *testing.T) {
	wd, err := os.Getwd() // .../internal/tool
	if err != nil {
		t.Fatal(err)
	}
	moduleRoot := filepath.Dir(filepath.Dir(wd)) // 上跳两级到项目根（含 go.mod）
	if _, err := os.Stat(filepath.Join(moduleRoot, "go.mod")); err != nil {
		t.Skipf("cannot locate module root from %s, skip: %v", wd, err)
	}

	sb := sandbox.New(sandbox.Config{WorkRoot: moduleRoot, Enabled: false})
	tl := NewDiagnostics(sb, moduleRoot, testTracer())

	if !tl.IsReadOnly(nil) || !tl.IsConcurrencySafe(nil) {
		t.Error("diagnostics should be read-only and concurrency-safe")
	}
	dec, _ := tl.CheckPermission(context.Background(), nil)
	if dec.Behavior != permission.BehaviorAllow {
		t.Errorf("CheckPermission behavior = %v, want allow", dec.Behavior)
	}

	res, _ := tl.Call(context.Background(), mustJSON(t, map[string]string{"path": "internal/tool"}), nil)
	for _, want := range []string{"## gofmt -l", "## go vet", "## go build"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("report missing section %q, got:\n%s", want, res.Content)
		}
	}
	if strings.Contains(res.Content, "exec error") {
		t.Errorf("unexpected exec error in report:\n%s", res.Content)
	}
}

func TestDiagnosticsTool_InvalidPathRejected(t *testing.T) {
	dir := t.TempDir()
	sb := sandbox.New(sandbox.Config{WorkRoot: dir, Enabled: false})
	tl := NewDiagnostics(sb, dir, testTracer())

	res, _ := tl.Call(context.Background(), mustJSON(t, map[string]string{"path": "sub; rm -rf /"}), nil)
	if !res.IsError {
		t.Error("expected invalid path with shell metacharacters to be rejected")
	}
}
