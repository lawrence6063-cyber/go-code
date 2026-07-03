package completion

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// TestParseAtToken 覆盖 @ token 解析的关键分支：无 @、光标处 @、片段、空白终止、多 @。
func TestParseAtToken(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		cursor  int
		start   int
		partial string
		active  bool
	}{
		{name: "no at", line: "hello world", cursor: 5, active: false},
		{name: "at only", line: "@", cursor: 1, start: 0, partial: "", active: true},
		{name: "partial", line: "see @int", cursor: 8, start: 4, partial: "int", active: true},
		{name: "space terminates", line: "@a b", cursor: 4, active: false},
		{name: "second at wins", line: "@a @b", cursor: 5, start: 3, partial: "b", active: true},
		{name: "cursor mid partial", line: "@abc", cursor: 2, start: 0, partial: "a", active: true},
		{name: "at after space", line: "hi @src/", cursor: 8, start: 3, partial: "src/", active: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseAtToken([]rune(tc.line), tc.cursor)
			if got.Active != tc.active {
				t.Fatalf("active=%v want %v", got.Active, tc.active)
			}
			if !tc.active {
				return
			}
			if got.Start != tc.start || got.Partial != tc.partial {
				t.Fatalf("got {start=%d partial=%q} want {start=%d partial=%q}",
					got.Start, got.Partial, tc.start, tc.partial)
			}
		})
	}
}

// TestParseAtToken_CursorBounds 验证越界光标被夹取到合法区间。
func TestParseAtToken_CursorBounds(t *testing.T) {
	line := []rune("@go")
	if got := ParseAtToken(line, 99); !got.Active || got.Partial != "go" {
		t.Fatalf("over-bound cursor: got %+v", got)
	}
	if got := ParseAtToken(line, -1); got.Active {
		t.Fatalf("negative cursor should be inactive: got %+v", got)
	}
}

// TestApplyChoice 验证 @partial 被替换为 @choice 并保留后缀、光标定位到路径末尾。
func TestApplyChoice(t *testing.T) {
	line := []rune("see @int rest")
	tok := ParseAtToken(line, 8) // partial = "int"
	newLine, newCursor := ApplyChoice(line, 8, tok.Start, "internal/tool/grep.go")
	want := "see @internal/tool/grep.go rest"
	if string(newLine) != want {
		t.Fatalf("newLine=%q want %q", string(newLine), want)
	}
	if newCursor != len("see @internal/tool/grep.go") {
		t.Fatalf("newCursor=%d want %d", newCursor, len("see @internal/tool/grep.go"))
	}
}

// TestApplyChoice_InvalidStart 验证 start 不指向 @ 时原样返回，不破坏输入。
func TestApplyChoice_InvalidStart(t *testing.T) {
	line := []rune("hello")
	got, cur := ApplyChoice(line, 3, 1, "x.go")
	if string(got) != "hello" || cur != 3 {
		t.Fatalf("invalid start should be no-op: got %q cursor=%d", string(got), cur)
	}
}

// TestRankMatches 验证匹配等级排序：前缀优先于子串，短路径优先。
func TestRankMatches(t *testing.T) {
	paths := []string{
		"internal/tool/grep.go",
		"grep.go",
		"docs/about_grep.md",
		"unrelated/file.txt",
	}
	got := rankMatches(paths, "grep")
	want := []string{"grep.go", "internal/tool/grep.go", "docs/about_grep.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rankMatches=%v want %v", got, want)
	}
}

// TestRankMatches_EmptyReturnsAll 验证空 partial 返回全部候选。
func TestRankMatches_EmptyReturnsAll(t *testing.T) {
	paths := []string{"a", "b"}
	if got := rankMatches(paths, ""); !reflect.DeepEqual(got, paths) {
		t.Fatalf("empty partial got %v want %v", got, paths)
	}
}

// TestProviderFilter_Walk 验证非 git 目录下 provider 走遍历并能过滤到目标文件。
func TestProviderFilter_Walk(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "main.go"), "package main")
	mustWrite(t, filepath.Join(dir, "sub", "helper.go"), "package sub")
	mustWrite(t, filepath.Join(dir, "node_modules", "junk.js"), "skip me")

	p := NewProvider(dir)
	got := p.Filter(context.Background(), "helper", 10)
	if len(got) != 1 || got[0] != "sub/helper.go" {
		t.Fatalf("filter helper=%v want [sub/helper.go]", got)
	}
	// node_modules 应被跳过
	for _, f := range p.Filter(context.Background(), "junk", 10) {
		t.Fatalf("node_modules should be skipped, got %q", f)
	}
}

// TestProviderFilter_Git 验证 git 仓库下优先用 git ls-files（含未跟踪）。
func TestProviderFilter_Git(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runInit(t, dir)
	mustWrite(t, filepath.Join(dir, "committed.go"), "package a")
	run(t, dir, "add", "committed.go")
	mustWrite(t, filepath.Join(dir, "fresh.go"), "package b")

	p := NewProvider(dir)
	if got := p.Filter(context.Background(), "committed", 10); len(got) != 1 || got[0] != "committed.go" {
		t.Fatalf("tracked=%v want [committed.go]", got)
	}
	if got := p.Filter(context.Background(), "fresh", 10); len(got) != 1 || got[0] != "fresh.go" {
		t.Fatalf("untracked=%v want [fresh.go]", got)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func runInit(t *testing.T, dir string) {
	t.Helper()
	run(t, dir, "init")
	run(t, dir, "config", "user.email", "test@example.com")
	run(t, dir, "config", "user.name", "test")
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
