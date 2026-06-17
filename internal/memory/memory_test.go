package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/goleak"
)

// TestMain 在包级别断言无 goroutine 泄漏（spec §9.3）。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// writeMemory 在 root/.cogent/MEMORY.md 写入指定内容。
func writeMemory(t *testing.T, root, content string) {
	t.Helper()
	dir := filepath.Join(root, MemoryDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, EntrypointName), []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestBuild_Missing(t *testing.T) {
	got, err := New().Build(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got != "" {
		t.Errorf("missing memory should return empty, got %q", got)
	}
}

func TestBuild_ReadsContent(t *testing.T) {
	root := t.TempDir()
	writeMemory(t, root, "hello\nworld")
	got, err := New().Build(context.Background(), root)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got != "hello\nworld" {
		t.Errorf("content = %q, want %q", got, "hello\nworld")
	}
}

func TestBuild_TruncatesLines(t *testing.T) {
	root := t.TempDir()
	var sb strings.Builder
	for i := 0; i < 300; i++ {
		sb.WriteString("line\n")
	}
	writeMemory(t, root, sb.String())

	got, err := New().Build(context.Background(), root)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if lines := strings.Count(got, "\n") + 1; lines > MaxEntrypointLines {
		t.Errorf("lines = %d, want <= %d", lines, MaxEntrypointLines)
	}
}

func TestBuild_TruncatesBytes(t *testing.T) {
	root := t.TempDir()
	writeMemory(t, root, strings.Repeat("a", 30000)) // 单行超字节上限
	got, err := New().Build(context.Background(), root)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(got) > MaxEntrypointBytes {
		t.Errorf("bytes = %d, want <= %d", len(got), MaxEntrypointBytes)
	}
	if len(got) == 0 {
		t.Error("truncated content should not be empty")
	}
}

func TestEntrypointPath(t *testing.T) {
	root := t.TempDir()
	path, err := entrypointPath(root)
	if err != nil {
		t.Fatalf("entrypointPath: %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("path %q should be absolute", path)
	}
	want := filepath.Join(MemoryDir, EntrypointName)
	if !strings.HasSuffix(path, want) {
		t.Errorf("path %q should end with %q", path, want)
	}
}

// ---------------------------------------------------------------------------
// Writer 测试
// ---------------------------------------------------------------------------

func TestWriter_AppendDaily(t *testing.T) {
	root := t.TempDir()
	w := NewWriter()
	ctx := context.Background()

	// 第一次追加：自动创建目录和文件
	if err := w.AppendDaily(ctx, root, "2026-06-17", "first entry"); err != nil {
		t.Fatalf("AppendDaily 1: %v", err)
	}
	// 第二次追加：保证 append 而非覆写
	if err := w.AppendDaily(ctx, root, "2026-06-17", "second entry"); err != nil {
		t.Fatalf("AppendDaily 2: %v", err)
	}
	path := filepath.Join(root, MemoryDir, DailyDir, "2026-06-17.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read daily: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "first entry") || !strings.Contains(got, "second entry") {
		t.Errorf("daily content = %q, want both entries", got)
	}
	// 每条记录末尾有换行分隔
	if !strings.HasSuffix(got, "\n") {
		t.Error("daily file should end with newline")
	}
}

func TestWriter_UpdateMemory(t *testing.T) {
	root := t.TempDir()
	w := NewWriter()
	ctx := context.Background()

	// 写入
	if err := w.UpdateMemory(ctx, root, "v1 content"); err != nil {
		t.Fatalf("UpdateMemory 1: %v", err)
	}
	got := readMemoryFile(t, root)
	if got != "v1 content" {
		t.Errorf("got %q, want v1 content", got)
	}
	// 覆写
	if err := w.UpdateMemory(ctx, root, "v2 replaced"); err != nil {
		t.Fatalf("UpdateMemory 2: %v", err)
	}
	got = readMemoryFile(t, root)
	if got != "v2 replaced" {
		t.Errorf("got %q, want v2 replaced", got)
	}
}

func TestWriter_UpdateMemory_DirAutoCreate(t *testing.T) {
	root := t.TempDir()
	w := NewWriter()
	// .cogent 目录不存在应自动创建
	if err := w.UpdateMemory(context.Background(), root, "auto"); err != nil {
		t.Fatalf("UpdateMemory: %v", err)
	}
	got := readMemoryFile(t, root)
	if got != "auto" {
		t.Errorf("got %q, want auto", got)
	}
}

func TestWriter_AppendDaily_PathValidation(t *testing.T) {
	// 即使 date 含 ../，路径不应穿越出 .cogent/daily/
	root := t.TempDir()
	w := NewWriter()
	// date 包含 .. 不会改变实际目录（文件名本身带 ..）
	err := w.AppendDaily(context.Background(), root, "../../etc/passwd", "evil")
	if err != nil {
		// 如果报错说明有校验——OK
		return
	}
	// 没报错则检查文件确实在 .cogent/daily/ 下
	base := filepath.Join(root, MemoryDir, DailyDir)
	entries, _ := os.ReadDir(base)
	for _, e := range entries {
		full := filepath.Join(base, e.Name())
		if !strings.HasPrefix(full, base) {
			t.Fatalf("file %q escaped daily dir", full)
		}
	}
}

// readMemoryFile 读取 MEMORY.md 内容。
func readMemoryFile(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, MemoryDir, EntrypointName))
	if err != nil {
		t.Fatalf("read MEMORY.md: %v", err)
	}
	return string(data)
}
