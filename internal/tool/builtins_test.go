package tool

import (
	"context"
	"encoding/json"
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
	tl := NewBash(sb, testTracer())
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
