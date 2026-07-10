package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/sandbox"
)

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	tl := NewReadFile(dir)
	if !tl.IsReadOnly(nil) || !tl.IsConcurrencySafe(nil) {
		t.Error("read_file should be read-only and concurrency-safe")
	}
	res, _ := tl.Call(context.Background(), mustJSON(t, map[string]string{"path": "a.txt"}), nil)
	if res.IsError || res.Content != "hello" {
		t.Errorf("read got %+v, want hello", res)
	}
	// 越界拒绝
	res, _ = tl.Call(context.Background(), mustJSON(t, map[string]string{"path": "../escape"}), nil)
	if !res.IsError {
		t.Error("expected escape to error")
	}
}

// TestReadFile_OffsetLimit 覆盖 P1 增强：按行范围流式读取大文件的中段/尾段。
func TestReadFile_OffsetLimit(t *testing.T) {
	dir := t.TempDir()
	lines := make([]string, 10)
	for i := range lines {
		lines[i] = fmt.Sprintf("line%d", i+1)
	}
	path := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tl := NewReadFile(dir)

	// 从第 3 行起读 2 行。
	res, _ := tl.Call(context.Background(), mustJSON(t, map[string]any{"path": "big.txt", "offset": 3, "limit": 2}), nil)
	if res.IsError {
		t.Fatalf("range read failed: %+v", res)
	}
	if !strings.Contains(res.Content, "line3") || !strings.Contains(res.Content, "line4") || strings.Contains(res.Content, "line5") {
		t.Errorf("range read content = %q, want lines 3-4 only", res.Content)
	}
	if !strings.Contains(res.Content, "[showing lines 3-4]") {
		t.Errorf("range read content missing range marker: %q", res.Content)
	}

	// 只传 limit，从头读 3 行。
	res, _ = tl.Call(context.Background(), mustJSON(t, map[string]any{"path": "big.txt", "limit": 3}), nil)
	if res.IsError || !strings.Contains(res.Content, "line1") || strings.Contains(res.Content, "line4") {
		t.Errorf("limit-only read got %+v, want lines 1-3", res)
	}

	// offset 超出文件行数：无匹配但不报错。
	res, _ = tl.Call(context.Background(), mustJSON(t, map[string]any{"path": "big.txt", "offset": 100}), nil)
	if res.IsError {
		t.Errorf("out-of-range offset should not error, got %+v", res)
	}

	// 未传 offset/limit 仍走 legacy 全文读，行为不变。
	res, _ = tl.Call(context.Background(), mustJSON(t, map[string]string{"path": "big.txt"}), nil)
	if res.IsError || !strings.Contains(res.Content, "line1") || !strings.Contains(res.Content, "line10") {
		t.Errorf("legacy full read regressed: %+v", res)
	}
}

func TestWriteFile_AndControlPlaneDeny(t *testing.T) {
	dir := t.TempDir()
	tl := NewWriteFile(dir)
	res, _ := tl.Call(context.Background(), mustJSON(t, map[string]string{"path": "out/x.txt", "content": "data"}), nil)
	if res.IsError {
		t.Fatalf("write failed: %+v", res)
	}
	got, err := os.ReadFile(filepath.Join(dir, "out/x.txt"))
	if err != nil || string(got) != "data" {
		t.Errorf("file content = %q err=%v, want data", got, err)
	}
	// 控制面写入：CheckPermission 应 Deny。
	dec, _ := tl.CheckPermission(context.Background(), mustJSON(t, map[string]string{"path": ".git/config", "content": "x"}))
	if dec.Behavior != permission.BehaviorDeny {
		t.Errorf("control-plane write behavior = %v, want deny", dec.Behavior)
	}
	// 普通写入：CheckPermission 应 Ask。
	dec, _ = tl.CheckPermission(context.Background(), mustJSON(t, map[string]string{"path": "y.txt", "content": "x"}))
	if dec.Behavior != permission.BehaviorAsk {
		t.Errorf("normal write behavior = %v, want ask", dec.Behavior)
	}
}

func TestEditFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "code.go")
	if err := os.WriteFile(path, []byte("func main() {\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tl := NewEditFile(dir)
	in := map[string]string{"path": "code.go", "old_string": "func main() {\n", "new_string": "func main() {\n\tprintln(\"hi\")\n"}
	res, _ := tl.Call(context.Background(), mustJSON(t, in), nil)
	if res.IsError {
		t.Fatalf("edit failed: %+v", res)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), `println("hi")`) {
		t.Errorf("edit not applied: %q", got)
	}
	// old_string 不存在 → 错误
	res, _ = tl.Call(context.Background(), mustJSON(t, map[string]string{"path": "code.go", "old_string": "NOPE", "new_string": "x"}), nil)
	if !res.IsError {
		t.Error("expected error for missing old_string")
	}
}

// TestEditFile_BatchAtomic 覆盖 P1 增强：一次调用传入多组 edits 按顺序原子应用；
// 任一组失败则整体不落盘（文件保持原样）。
func TestEditFile_BatchAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "code.go")
	original := "func main() {\n\tfoo()\n\tbar()\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	tl := NewEditFile(dir)

	// 成功场景：两条编辑按顺序应用，第二条匹配第一条修改后的文本。
	in := map[string]any{
		"path": "code.go",
		"edits": []map[string]string{
			{"old_string": "foo()", "new_string": "foo(1)"},
			{"old_string": "bar()", "new_string": "bar(2)"},
		},
	}
	res, _ := tl.Call(context.Background(), mustJSON(t, in), nil)
	if res.IsError {
		t.Fatalf("batch edit failed: %+v", res)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "foo(1)") || !strings.Contains(string(got), "bar(2)") {
		t.Errorf("batch edit not fully applied: %q", got)
	}

	// 失败场景：第二条 old_string 不唯一（出现 0 次），整体不落盘。
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	in = map[string]any{
		"path": "code.go",
		"edits": []map[string]string{
			{"old_string": "foo()", "new_string": "foo(1)"},
			{"old_string": "NOPE", "new_string": "x"},
		},
	}
	res, _ = tl.Call(context.Background(), mustJSON(t, in), nil)
	if !res.IsError {
		t.Error("expected batch edit to fail when any edit's old_string is not unique")
	}
	got, _ = os.ReadFile(path)
	if string(got) != original {
		t.Errorf("failed batch edit must not partially write file, got %q", got)
	}
}

func TestListDir(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644)
	_ = os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	tl := NewListDir(dir)
	res, _ := tl.Call(context.Background(), mustJSON(t, map[string]string{"path": ""}), nil)
	if res.IsError {
		t.Fatalf("list failed: %+v", res)
	}
	if !strings.Contains(res.Content, "f.txt") || !strings.Contains(res.Content, "sub/") {
		t.Errorf("list content = %q, want f.txt and sub/", res.Content)
	}
}

func TestGrep(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\nfunc Foo() {}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b.go"), []byte("package main\nfunc Bar() {}\n"), 0o644)
	tl := NewGrep(dir)
	res, _ := tl.Call(context.Background(), mustJSON(t, map[string]string{"pattern": "func Foo"}), nil)
	if res.IsError {
		t.Fatalf("grep failed: %+v", res)
	}
	if !strings.Contains(res.Content, "a.go:2:") {
		t.Errorf("grep content = %q, want a.go:2 match", res.Content)
	}
	// 无匹配
	res, _ = tl.Call(context.Background(), mustJSON(t, map[string]string{"pattern": "zzzz"}), nil)
	if res.IsError || res.Content != "no matches" {
		t.Errorf("grep no-match got %+v", res)
	}
}

func TestBash_DangerousBlockedAndRun(t *testing.T) {
	dir := t.TempDir()
	sb := sandbox.New(sandbox.Config{WorkRoot: dir, Enabled: true})
	tl := NewBash(sb, dir, testTracer())
	// 危险命令 CheckPermission 应 Deny。
	dec, _ := tl.CheckPermission(context.Background(), mustJSON(t, map[string]string{"command": "rm -rf /"}))
	if dec.Behavior != permission.BehaviorDeny {
		t.Errorf("dangerous behavior = %v, want deny", dec.Behavior)
	}
	// 普通命令 CheckPermission 应 Ask。
	dec, _ = tl.CheckPermission(context.Background(), mustJSON(t, map[string]string{"command": "echo hi"}))
	if dec.Behavior != permission.BehaviorAsk {
		t.Errorf("normal behavior = %v, want ask", dec.Behavior)
	}
	// 执行普通命令。
	res, _ := tl.Call(context.Background(), mustJSON(t, map[string]string{"command": "echo cogent"}), nil)
	if res.IsError || !strings.Contains(res.Content, "cogent") {
		t.Errorf("bash echo got %+v, want output cogent", res)
	}
	// 危险命令在 Call 兜底拦截。
	res, _ = tl.Call(context.Background(), mustJSON(t, map[string]string{"command": "rm -rf /"}), nil)
	if !res.IsError {
		t.Error("dangerous command should be blocked in Call")
	}
}

// TestBash_ControlPlaneCommandBlocked 验证 P0 修复：bash 不能再借重定向/rm 绕开
// write_file/edit_file 已有的控制面写保护（.cogent/.git）。
func TestBash_ControlPlaneCommandBlocked(t *testing.T) {
	dir := t.TempDir()
	sb := sandbox.New(sandbox.Config{WorkRoot: dir, Enabled: true})
	tl := NewBash(sb, dir, testTracer())

	cases := []string{
		"echo evil > .cogent/skills/x/SKILL.md",
		"rm -rf .cogent",
		"mv secret.txt .git/hooks/pre-commit",
	}
	for _, cmd := range cases {
		dec, _ := tl.CheckPermission(context.Background(), mustJSON(t, map[string]string{"command": cmd}))
		if dec.Behavior != permission.BehaviorDeny {
			t.Errorf("CheckPermission(%q) behavior = %v, want deny", cmd, dec.Behavior)
		}
		res, _ := tl.Call(context.Background(), mustJSON(t, map[string]string{"command": cmd}), nil)
		if !res.IsError {
			t.Errorf("Call(%q) should be blocked, got %+v", cmd, res)
		}
	}
	// 普通命令不受影响。
	dec, _ := tl.CheckPermission(context.Background(), mustJSON(t, map[string]string{"command": "echo hi > out.txt"}))
	if dec.Behavior != permission.BehaviorAsk {
		t.Errorf("normal redirect behavior = %v, want ask", dec.Behavior)
	}
}
