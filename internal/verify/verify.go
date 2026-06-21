// Package verify 提供独立于执行体的目标终止条件判定器（LOOP_SPEC §4.1）。
// 判定器与 engine 解耦：执行体（写代码的 Agent）无法篡改判定结果（独立判定不变量）。
// 本包是依赖图的叶子，仅依赖 sandbox / 标准库，便于单测注入替身。
package verify

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/alaindong/cogent/internal/sandbox"
)

// Report 是一次判定的结构化结论。
type Report struct {
	Passed  bool   // 是否达成目标（fail-closed：判定异常一律视为 false）
	Summary string // 人类可读结论；未通过时作为下一轮反馈注入 Agent
	Detail  string // 原始证据（如脚本 stdout/stderr），落 trace 前应脱敏
}

// Verifier 判定目标是否达成。实现应是确定性、可复现、独立于被测 Agent 的。
type Verifier interface {
	// Verify 在 workRoot 上判定目标是否达成；返回的 error 仅表示判定过程本身失败
	// （此时按 fail-closed 处理，Report.Passed 必为 false），与「目标未达成」
	// （Report.Passed=false 且 error 为 nil）语义区分。
	Verify(ctx context.Context, workRoot, goalIntent string) (Report, error)
}

// VerifierFunc 把普通函数适配为 Verifier，便于组合与测试（如 L0 阶段的恒达标桩）。
type VerifierFunc func(ctx context.Context, workRoot, goalIntent string) (Report, error)

// Verify 见 Verifier 接口说明。
func (f VerifierFunc) Verify(ctx context.Context, workRoot, goalIntent string) (Report, error) {
	return f(ctx, workRoot, goalIntent)
}

// ScriptVerifier 把 eval 的 verify.sh 升级为主循环里的独立判定器：经沙箱执行验收脚本，
// 退出码 0 = 通过；stdout/stderr 收敛进 Report.Detail。这是把 DEV_SPEC §8.8 的客观判据
// 从评估框架接回主循环的关键一步。脚本是开发者提供的可信控制面，与不可信的模型输出隔离。
type ScriptVerifier struct {
	Script  string          // 验收脚本路径（如 eval/tasks/<name>/verify.sh）
	Sandbox sandbox.Sandbox // 固定沙箱（NewSandbox 为 nil 时使用，忽略传入的 workRoot；向后兼容）
	// NewSandbox 可选：按传入 workRoot 构造受限沙箱，使验收脚本跑在改动所在目录
	// （如 worktree 根），让客观判据能看到 maker 的真实改动。为 nil 时回退固定 Sandbox。
	NewSandbox func(workRoot string) sandbox.Sandbox
}

// NewScriptVerifier 构造一个脚本判定器。script 为空或 sb 为 nil 时仍可构造，
// 但 Verify 会因配置缺失返回错误并按 fail-closed 处理。
func NewScriptVerifier(script string, sb sandbox.Sandbox) ScriptVerifier {
	return ScriptVerifier{Script: script, Sandbox: sb}
}

// Verify 见 Verifier 接口说明：经沙箱执行脚本，退出码 0=通过；执行失败一律 fail-closed=未通过。
// 设有 NewSandbox 时按传入 workRoot 构造沙箱，使脚本跑在改动所在目录；否则用固定沙箱。
func (v ScriptVerifier) Verify(ctx context.Context, workRoot, _ /*goalIntent*/ string) (Report, error) {
	if strings.TrimSpace(v.Script) == "" {
		return Report{Summary: "no verify script configured"}, errors.New("empty verify script")
	}
	sb := v.Sandbox
	if v.NewSandbox != nil {
		sb = v.NewSandbox(workRoot)
	}
	if sb == nil {
		return Report{Summary: "no sandbox configured"}, errors.New("nil sandbox")
	}
	res, err := sb.Exec(ctx, "bash "+v.Script)
	detail := strings.TrimSpace(strings.TrimSpace(res.Stdout) + "\n" + strings.TrimSpace(res.Stderr))
	if err != nil {
		return Report{Summary: "verifier failed to run: " + err.Error(), Detail: detail},
			fmt.Errorf("verify exec: %w", err)
	}
	if res.ExitCode == 0 {
		return Report{Passed: true, Summary: "verification passed (exit code 0)", Detail: detail}, nil
	}
	return Report{
		Summary: fmt.Sprintf("verification not passed (exit code %d)", res.ExitCode),
		Detail:  detail,
	}, nil
}
