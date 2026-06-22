package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitSnapshotter_IsGitRepo(t *testing.T) {
	// 在临时目录中 git init，验证 IsGitRepo 返回 true
	dir := t.TempDir()
	initGit(t, dir)

	snap := NewGitSnapshotter(dir)
	if !snap.IsGitRepo() {
		t.Error("IsGitRepo() = false, want true")
	}
}

func TestGitSnapshotter_IsGitRepo_NonGit(t *testing.T) {
	// 非 git 目录应返回 false
	dir := t.TempDir()
	snap := NewGitSnapshotter(dir)
	if snap.IsGitRepo() {
		t.Error("IsGitRepo() = true, want false")
	}
}

func TestGitSnapshotter_Take_NoChanges(t *testing.T) {
	// 工作区无改动时 Take 应返回空字符串
	dir := t.TempDir()
	initGitWithCommit(t, dir)

	snap := NewGitSnapshotter(dir)
	id, err := snap.Take(context.Background())
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if id != "" {
		t.Errorf("Take() = %q, want empty (no changes)", id)
	}
}

func TestGitSnapshotter_Take_WithChanges(t *testing.T) {
	// 工作区有改动时 Take 应返回非空 SHA
	dir := t.TempDir()
	initGitWithCommit(t, dir)

	// 创建一个新文件模拟改动
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	snap := NewGitSnapshotter(dir)
	id, err := snap.Take(context.Background())
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if id == "" {
		t.Error("Take() = empty, want non-empty SHA")
	}
}

func TestGitSnapshotter_Restore_EmptyID(t *testing.T) {
	// Restore 空 ID 应执行 discard all（checkout + clean）
	dir := t.TempDir()
	initGitWithCommit(t, dir)

	// 创建未跟踪文件和修改已跟踪文件
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "init.txt"), []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}

	snap := NewGitSnapshotter(dir)
	if err := snap.Restore(context.Background(), ""); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// 验证未跟踪文件被清理
	if _, err := os.Stat(filepath.Join(dir, "untracked.txt")); !os.IsNotExist(err) {
		t.Error("untracked.txt still exists after Restore")
	}
	// 验证已跟踪文件被恢复
	content, err := os.ReadFile(filepath.Join(dir, "init.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "initial" {
		t.Errorf("init.txt content = %q, want %q", string(content), "initial")
	}
}

func TestGitSnapshotter_TakeAndRestore(t *testing.T) {
	// 完整流程：有改动 → Take → 新增更多改动 → Restore → 验证恢复到 Take 时的状态
	dir := t.TempDir()
	initGitWithCommit(t, dir)

	// 第一次改动（模拟用户在 agent 执行前的改动）
	if err := os.WriteFile(filepath.Join(dir, "user.txt"), []byte("user change"), 0o644); err != nil {
		t.Fatal(err)
	}

	snap := NewGitSnapshotter(dir)
	id, err := snap.Take(context.Background())
	if err != nil {
		t.Fatalf("Take: %v", err)
	}

	// 第二次改动（模拟 agent 执行的改动）
	if err := os.WriteFile(filepath.Join(dir, "agent.txt"), []byte("agent change"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "init.txt"), []byte("agent modified"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Restore 到 Take 时的状态
	if err := snap.Restore(context.Background(), id); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// 验证 agent 的改动被撤销
	if _, err := os.Stat(filepath.Join(dir, "agent.txt")); !os.IsNotExist(err) {
		t.Error("agent.txt still exists after Restore")
	}

	// 验证用户的改动被恢复
	content, err := os.ReadFile(filepath.Join(dir, "user.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "user change" {
		t.Errorf("user.txt content = %q, want %q", string(content), "user change")
	}
}

// initGit 在目录中执行 git init。
func initGit(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	// 配置 user 信息（某些环境下 git commit 需要）
	for _, args := range [][]string{
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// initGitWithCommit 在目录中 git init 并创建一个初始提交。
func initGitWithCommit(t *testing.T, dir string) {
	t.Helper()
	initGit(t, dir)
	// 创建初始文件并提交
	if err := os.WriteFile(filepath.Join(dir, "init.txt"), []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "."},
		{"commit", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}
