//go:build integration

// 本文件是 worktree 的集成测试（LOOP_SPEC §7.2 集成层），用真实 git 仓库验证
// create/merge/discard/冲突 的端到端行为，与单元测试分离（需本机有 git）。
// 运行：go test -tags=integration ./internal/worktree/...
package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alaindong/cogent/internal/sandbox"
)

// initRepo 在 t.TempDir() 起一个最小 git 仓库（含一次初始提交，默认分支 main）。
func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@cogent.local")
	runGit(t, repo, "config", "user.name", "cogent test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "init")
	return repo
}

// runGit 直接执行 git（测试夹具用，不经 sandbox）。
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// newManager 用绑定到 repo 根的真实 sandbox（Enabled=false 继承宿主 PATH 跑 git）构造管理器，
// worktree 目录放在 repo 同级临时区。
func newManager(t *testing.T, repo string) Manager {
	t.Helper()
	sb := sandbox.New(sandbox.Config{WorkRoot: repo, Enabled: false})
	return NewWithBaseDir(sb, t.TempDir())
}

func TestIntegration_CreateMergeRoundTrip(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ctx := context.Background()

	ws, err := m.Create(ctx, "main")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// worktree 是独立目录，maker 在其中写而不触碰主仓库工作区。
	if _, err := os.Stat(ws.Root); err != nil {
		t.Fatalf("worktree dir missing: %v", err)
	}
	newFile := filepath.Join(ws.Root, "feature.txt")
	if err := os.WriteFile(newFile, []byte("from worktree\n"), 0o644); err != nil {
		t.Fatalf("write in worktree: %v", err)
	}
	// 主仓库此刻不应看到该文件（物理隔离）。
	if _, err := os.Stat(filepath.Join(repo, "feature.txt")); err == nil {
		t.Fatal("main repo should not see worktree-only file before merge")
	}

	if err := m.Merge(ctx, ws, "main"); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	// 合并后主仓库可见。
	if _, err := os.Stat(filepath.Join(repo, "feature.txt")); err != nil {
		t.Errorf("merged file not in main repo: %v", err)
	}
}

func TestIntegration_DiscardRemovesWorktreeAndBranch(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ctx := context.Background()

	ws, err := m.Create(ctx, "main")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := m.Discard(ctx, ws); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if _, err := os.Stat(ws.Root); !os.IsNotExist(err) {
		t.Errorf("worktree dir should be removed, stat err = %v", err)
	}
	branches := runGit(t, repo, "branch", "--list", ws.Branch)
	if strings.TrimSpace(branches) != "" {
		t.Errorf("branch %s should be deleted, got: %q", ws.Branch, branches)
	}
}

func TestIntegration_MergeConflictReturnsSentinel(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ctx := context.Background()

	ws, err := m.Create(ctx, "main")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 在 worktree 改 README，同时在主仓库对同一文件提交不同内容，制造合并冲突。
	if err := os.WriteFile(filepath.Join(ws.Root, "README.md"), []byte("worktree side\n"), 0o644); err != nil {
		t.Fatalf("write worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("main side\n"), 0o644); err != nil {
		t.Fatalf("write main: %v", err)
	}
	runGit(t, repo, "commit", "-q", "-am", "main change")

	err = m.Merge(ctx, ws, "main")
	if err == nil {
		t.Fatal("expected merge conflict")
	}
	if !strings.Contains(err.Error(), "merge conflict") {
		t.Errorf("err = %v, want ErrMergeConflict", err)
	}
	// 冲突已 abort：主仓库工作区应干净（无未完成合并）。
	status := runGit(t, repo, "status", "--porcelain")
	if strings.Contains(status, "UU") {
		t.Errorf("merge should be aborted, status: %q", status)
	}
}
