package worktree

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alaindong/cogent/internal/sandbox"
	"go.uber.org/goleak"
)

// TestMain 在包级别断言无 goroutine 泄漏（DEV_SPEC §9.3）。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fakeSandbox 记录收到的命令并按注入规则返回结果，免真实 git，使命令构造可确定性断言。
type fakeSandbox struct {
	cmds     []string
	resultFn func(cmd string) (sandbox.ExecResult, error)
}

func (f *fakeSandbox) ShouldSandbox(string) bool { return false }

func (f *fakeSandbox) Exec(_ context.Context, command string) (sandbox.ExecResult, error) {
	f.cmds = append(f.cmds, command)
	if f.resultFn != nil {
		return f.resultFn(command)
	}
	return sandbox.ExecResult{}, nil
}

// findCmd 返回首个包含 sub 的已记录命令。
func (f *fakeSandbox) findCmd(sub string) (string, bool) {
	for _, c := range f.cmds {
		if strings.Contains(c, sub) {
			return c, true
		}
	}
	return "", false
}

func TestCreate_BuildsWorktreeAddAndReturnsWorkspace(t *testing.T) {
	fs := &fakeSandbox{}
	m := NewWithBaseDir(fs, "/tmp/wt")

	ws, err := m.Create(context.Background(), "main")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(ws.Branch, branchPrefix) {
		t.Errorf("branch = %q, want prefix %q", ws.Branch, branchPrefix)
	}
	if !strings.HasPrefix(ws.Root, "/tmp/wt/"+dirPrefix) {
		t.Errorf("root = %q, want under baseDir with prefix", ws.Root)
	}
	cmd, ok := fs.findCmd("git worktree add -b")
	if !ok {
		t.Fatalf("no worktree add command recorded: %v", fs.cmds)
	}
	if !strings.Contains(cmd, "main") {
		t.Errorf("worktree add missing baseRef: %q", cmd)
	}
}

func TestCreate_GitErrorPropagates(t *testing.T) {
	fs := &fakeSandbox{resultFn: func(string) (sandbox.ExecResult, error) {
		return sandbox.ExecResult{ExitCode: 128, Stderr: "fatal: invalid reference: nope"}, nil
	}}
	m := NewWithBaseDir(fs, t.TempDir())

	if _, err := m.Create(context.Background(), "nope"); err == nil {
		t.Fatal("Create should fail on non-zero git exit")
	}
}

func TestMerge_CommitsThenMerges(t *testing.T) {
	fs := &fakeSandbox{}
	m := NewWithBaseDir(fs, t.TempDir())

	ws := Workspace{Root: "/tmp/wt/cogent-wt-abc", Branch: "cogent/wt-abc"}
	if err := m.Merge(context.Background(), ws, "main"); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if _, ok := fs.findCmd("git -C '/tmp/wt/cogent-wt-abc' add -A"); !ok {
		t.Errorf("no commit-in-worktree command recorded: %v", fs.cmds)
	}
	if _, ok := fs.findCmd("git merge --no-ff --no-edit 'cogent/wt-abc'"); !ok {
		t.Errorf("no merge command recorded: %v", fs.cmds)
	}
}

func TestMerge_ConflictReturnsSentinelAndAborts(t *testing.T) {
	fs := &fakeSandbox{resultFn: func(cmd string) (sandbox.ExecResult, error) {
		if strings.HasPrefix(cmd, "git merge --no-ff") {
			return sandbox.ExecResult{ExitCode: 1, Stderr: "CONFLICT (content): Merge conflict in a.go"}, nil
		}
		return sandbox.ExecResult{}, nil
	}}
	m := NewWithBaseDir(fs, t.TempDir())

	ws := Workspace{Root: "/tmp/x", Branch: "cogent/wt-x"}
	err := m.Merge(context.Background(), ws, "main")
	if err == nil {
		t.Fatal("Merge should fail on conflict")
	}
	if !strings.Contains(err.Error(), "merge conflict") {
		t.Errorf("err = %v, want wraps ErrMergeConflict", err)
	}
	if _, ok := fs.findCmd("git merge --abort"); !ok {
		t.Errorf("conflict should trigger merge --abort: %v", fs.cmds)
	}
}

func TestMerge_EmptyWorkspaceRejected(t *testing.T) {
	m := NewWithBaseDir(&fakeSandbox{}, t.TempDir())
	if err := m.Merge(context.Background(), Workspace{}, "main"); err == nil {
		t.Fatal("Merge should reject empty workspace")
	}
}

func TestDiscard_RemovesWorktreeAndBranch(t *testing.T) {
	fs := &fakeSandbox{}
	m := NewWithBaseDir(fs, t.TempDir())

	ws := Workspace{Root: "/tmp/wt/cogent-wt-z", Branch: "cogent/wt-z"}
	if err := m.Discard(context.Background(), ws); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if _, ok := fs.findCmd("git worktree remove --force '/tmp/wt/cogent-wt-z'"); !ok {
		t.Errorf("no worktree remove recorded: %v", fs.cmds)
	}
	if _, ok := fs.findCmd("git branch -D 'cogent/wt-z'"); !ok {
		t.Errorf("no branch delete recorded: %v", fs.cmds)
	}
}

func TestEnsureClean_PassesOnEmptyStatus(t *testing.T) {
	fs := &fakeSandbox{resultFn: func(string) (sandbox.ExecResult, error) {
		return sandbox.ExecResult{Stdout: "", ExitCode: 0}, nil
	}}
	if err := EnsureClean(context.Background(), fs); err != nil {
		t.Fatalf("EnsureClean on clean tree: %v", err)
	}
	if _, ok := fs.findCmd("git status --porcelain"); !ok {
		t.Errorf("expected git status --porcelain probe, got: %v", fs.cmds)
	}
}

func TestEnsureClean_RejectsDirtyTree(t *testing.T) {
	fs := &fakeSandbox{resultFn: func(string) (sandbox.ExecResult, error) {
		return sandbox.ExecResult{Stdout: " M cost.go\n?? findfiles.go\n", ExitCode: 0}, nil
	}}
	err := EnsureClean(context.Background(), fs)
	if err == nil {
		t.Fatal("EnsureClean should reject dirty tree")
	}
	if !errors.Is(err, ErrDirtyWorktree) {
		t.Errorf("err = %v, want wraps ErrDirtyWorktree", err)
	}
	if !strings.Contains(err.Error(), "2 ") {
		t.Errorf("err = %v, want report 2 dirty paths", err)
	}
}

func TestEnsureClean_PropagatesGitError(t *testing.T) {
	fs := &fakeSandbox{resultFn: func(string) (sandbox.ExecResult, error) {
		return sandbox.ExecResult{ExitCode: 128, Stderr: "fatal: not a git repository"}, nil
	}}
	err := EnsureClean(context.Background(), fs)
	if err == nil {
		t.Fatal("EnsureClean should fail when git status exits non-zero")
	}
	if errors.Is(err, ErrDirtyWorktree) {
		t.Errorf("non-zero git exit should not be reported as dirty: %v", err)
	}
}

func TestShellQuote(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"main", "'main'"},
		{"feature/x", "'feature/x'"},
		{"a b", "'a b'"},
		{"it's", `'it'\''s'`},
	}
	for _, c := range cases {
		if got := shellQuote(c.in); got != c.want {
			t.Errorf("shellQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
