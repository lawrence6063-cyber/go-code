package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestShouldSandbox(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		command string
		want    bool
	}{
		{"disabled", Config{Enabled: false}, "ls", false},
		{"enabled default", Config{Enabled: true}, "ls -la", true},
		{"excluded first word", Config{Enabled: true, ExcludedCommands: []string{"git"}}, "git status", false},
		{"excluded not matched", Config{Enabled: true, ExcludedCommands: []string{"git"}}, "ls", true},
		{"empty command", Config{Enabled: true}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sb := New(tt.cfg)
			if got := sb.ShouldSandbox(tt.command); got != tt.want {
				t.Errorf("ShouldSandbox(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestExec_EchoStdoutAndExitCode(t *testing.T) {
	dir := t.TempDir()
	sb := New(Config{WorkRoot: dir, Enabled: true})

	res, err := sb.Exec(context.Background(), "echo cogent")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", res.ExitCode)
	}
	if res.Stdout != "cogent\n" {
		t.Errorf("stdout = %q, want %q", res.Stdout, "cogent\n")
	}
}

func TestExec_NonZeroExitAndStderr(t *testing.T) {
	dir := t.TempDir()
	sb := New(Config{WorkRoot: dir, Enabled: true})

	res, err := sb.Exec(context.Background(), "echo oops >&2; exit 3")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", res.ExitCode)
	}
	if res.Stderr != "oops\n" {
		t.Errorf("stderr = %q, want %q", res.Stderr, "oops\n")
	}
}

func TestExec_DangerousCommandBlocked(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		cfg  Config
		cmd  string
	}{
		{"builtin rm rf", Config{WorkRoot: dir, Enabled: true}, "rm -rf /"},
		{"denied rule", Config{WorkRoot: dir, Enabled: true, DeniedRules: []string{"forbidden"}}, "echo forbidden-thing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sb := New(tt.cfg)
			_, err := sb.Exec(context.Background(), tt.cmd)
			if !errors.Is(err, ErrDangerousCommand) {
				t.Errorf("err = %v, want ErrDangerousCommand", err)
			}
		})
	}
}

func TestExec_Timeout(t *testing.T) {
	dir := t.TempDir()
	sb := New(Config{WorkRoot: dir, Enabled: true, Timeout: 50 * time.Millisecond})

	start := time.Now()
	res, err := sb.Exec(context.Background(), "sleep 5")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("timeout not enforced")
	}
	if res.ExitCode == 0 {
		t.Error("timed-out command should have non-zero exit code")
	}
}

func TestScrubBareGitRepoFiles_RemovesFreshArtifacts(t *testing.T) {
	dir := t.TempDir()
	sb := New(Config{WorkRoot: dir, Enabled: true})

	// 命令在工作根目录顶层伪造出完整 bare-repo 签名。
	_, err := sb.Exec(context.Background(), "mkdir objects refs && echo ref: refs/heads/main > HEAD && echo x > config")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	for _, name := range []string{"HEAD", "objects", "refs", "config"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("artifact %q should have been scrubbed (err=%v)", name, err)
		}
	}
}

func TestScrubBareGitRepoFiles_PreservesExistingGit(t *testing.T) {
	dir := t.TempDir()
	// 预置一个既有 .git 目录及内容。
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sb := New(Config{WorkRoot: dir, Enabled: true})

	if _, err := sb.Exec(context.Background(), "echo hi"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if _, err := os.Stat(filepath.Join(gitDir, "HEAD")); err != nil {
		t.Errorf("existing .git/HEAD must be preserved, got err=%v", err)
	}
}

func TestScrubBareGitRepoFiles_KeepsIncompleteSignature(t *testing.T) {
	dir := t.TempDir()
	sb := New(Config{WorkRoot: dir, Enabled: true})

	// 仅创建 HEAD（签名不完整），不应触发清理。
	if _, err := sb.Exec(context.Background(), "echo ref > HEAD"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		t.Errorf("incomplete signature must not be scrubbed, got err=%v", err)
	}
}
