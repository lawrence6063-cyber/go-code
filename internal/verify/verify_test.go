package verify

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/goleak"

	"github.com/alaindong/cogent/internal/sandbox"
)

// TestMain 在包级别断言无 goroutine 泄漏（spec §9.3）。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fakeSandbox 是 sandbox.Sandbox 的脚本化替身：返回预设的执行结果与错误，
// 并记录最近一次收到的命令，供断言判定逻辑而无需真实执行进程。
type fakeSandbox struct {
	res     sandbox.ExecResult
	err     error
	gotCmd  string
	execHit int
}

func (f *fakeSandbox) ShouldSandbox(string) bool { return true }

func (f *fakeSandbox) Exec(_ context.Context, command string) (sandbox.ExecResult, error) {
	f.gotCmd = command
	f.execHit++
	return f.res, f.err
}

func TestScriptVerifier_Verify(t *testing.T) {
	tests := []struct {
		name       string
		script     string
		sb         sandbox.Sandbox
		wantPassed bool
		wantErr    bool
	}{
		{
			name:       "exit zero passes",
			script:     "/tmp/verify.sh",
			sb:         &fakeSandbox{res: sandbox.ExecResult{ExitCode: 0, Stdout: "PASS"}},
			wantPassed: true,
			wantErr:    false,
		},
		{
			name:       "non-zero exit not passed",
			script:     "/tmp/verify.sh",
			sb:         &fakeSandbox{res: sandbox.ExecResult{ExitCode: 1, Stderr: "FAIL"}},
			wantPassed: false,
			wantErr:    false,
		},
		{
			name:       "exec error fail-closed",
			script:     "/tmp/verify.sh",
			sb:         &fakeSandbox{err: errors.New("boom"), res: sandbox.ExecResult{ExitCode: -1}},
			wantPassed: false,
			wantErr:    true,
		},
		{
			name:       "empty script fail-closed",
			script:     "   ",
			sb:         &fakeSandbox{res: sandbox.ExecResult{ExitCode: 0}},
			wantPassed: false,
			wantErr:    true,
		},
		{
			name:       "nil sandbox fail-closed",
			script:     "/tmp/verify.sh",
			sb:         nil,
			wantPassed: false,
			wantErr:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := NewScriptVerifier(tt.script, tt.sb)
			report, err := v.Verify(context.Background(), "/work", "fix the bug")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if report.Passed != tt.wantPassed {
				t.Errorf("Passed = %v, want %v", report.Passed, tt.wantPassed)
			}
		})
	}
}

func TestScriptVerifier_RunsScriptViaBash(t *testing.T) {
	sb := &fakeSandbox{res: sandbox.ExecResult{ExitCode: 0}}
	v := NewScriptVerifier("/tmp/verify.sh", sb)
	if _, err := v.Verify(context.Background(), "/work", "goal"); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if want := "bash /tmp/verify.sh"; sb.gotCmd != want {
		t.Errorf("command = %q, want %q", sb.gotCmd, want)
	}
}

func TestVerifierFunc_Adapts(t *testing.T) {
	var got string
	var v Verifier = VerifierFunc(func(_ context.Context, workRoot, _ string) (Report, error) {
		got = workRoot
		return Report{Passed: true, Summary: "ok"}, nil
	})
	report, err := v.Verify(context.Background(), "/work", "goal")
	if err != nil || !report.Passed {
		t.Fatalf("report = %+v, err = %v", report, err)
	}
	if got != "/work" {
		t.Errorf("workRoot = %q, want /work", got)
	}
}

func TestScriptVerifier_NewSandboxHonorsWorkRoot(t *testing.T) {
	var gotRoot string
	sb := &fakeSandbox{res: sandbox.ExecResult{ExitCode: 0, Stdout: "PASS"}}
	v := ScriptVerifier{
		Script: "/tmp/verify.sh",
		NewSandbox: func(workRoot string) sandbox.Sandbox {
			gotRoot = workRoot
			return sb
		},
	}
	report, err := v.Verify(context.Background(), "/private/var/cogent-wt-1", "goal")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !report.Passed {
		t.Errorf("Passed = false, want true")
	}
	if gotRoot != "/private/var/cogent-wt-1" {
		t.Errorf("factory workRoot = %q, want worktree root", gotRoot)
	}
	if sb.execHit != 1 {
		t.Errorf("exec hit = %d, want 1 (factory sandbox used)", sb.execHit)
	}
}

func TestScriptVerifier_NilSandboxFromFactoryFailsClosed(t *testing.T) {
	v := ScriptVerifier{
		Script:     "/tmp/verify.sh",
		NewSandbox: func(string) sandbox.Sandbox { return nil },
	}
	report, err := v.Verify(context.Background(), "/work", "goal")
	if err == nil {
		t.Error("nil sandbox from factory should fail-closed with error")
	}
	if report.Passed {
		t.Error("nil sandbox should not pass")
	}
}
