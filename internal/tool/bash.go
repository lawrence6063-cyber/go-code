// Package tool 中的 bash.go 实现 bash 工具：经统一的 sandbox.Sandbox 执行 shell 命令，
// 由沙箱负责危险命令拦截、工作目录约束、超时与执行后清理；本文件只做入参解析、span 埋点与输出格式化。
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/sandbox"
	"github.com/alaindong/cogent/internal/types"
)

// maxBashOutput 是回流输出上限，防止撑爆上下文。
const maxBashOutput = 32 * 1024

// bashTool 经 sandbox 在工作目录内执行 shell 命令；默认非只读、权限 ask、危险命令直接拒绝。
type bashTool struct {
	Defaults
	sb       sandbox.Sandbox
	workRoot string
	tracer   observe.Tracer
}

// NewBash 用沙箱、工作根目录与 tracer 构造 bash 工具；workRoot 用于 CheckPermission 阶段
// 提前识别控制面写入目标（与 sandbox.Exec 内的执行期兜底形成纵深，二者共用同一套判定）。
func NewBash(sb sandbox.Sandbox, workRoot string, tracer observe.Tracer) Tool {
	return &bashTool{sb: sb, workRoot: workRoot, tracer: tracer}
}

func (t *bashTool) Name() string { return "bash" }
func (t *bashTool) Description() string {
	return "Run a shell command in the workspace root and return its output."
}

func (t *bashTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"shell command to execute"}},"required":["command"]}`)
}

// CheckPermission 危险命令与控制面写入目标直接拒绝，其余走默认 ask
// （与 sandbox.Exec 内的确定性拦截形成双重兜底）。
func (t *bashTool) CheckPermission(_ context.Context, input json.RawMessage) (permission.Decision, error) {
	cmd, err := parseCommand(input)
	if err != nil {
		return permission.Decision{Behavior: permission.BehaviorDeny, Reason: "invalid command input"}, nil
	}
	if sandbox.IsDangerousCommand(cmd) {
		return permission.Decision{Behavior: permission.BehaviorDeny, Reason: "dangerous command blocked"}, nil
	}
	if sandbox.IsControlPlaneCommandTarget(t.workRoot, cmd) {
		return permission.Decision{Behavior: permission.BehaviorDeny, Reason: "command targets control-plane path"}, nil
	}
	return permission.Decision{Behavior: permission.BehaviorAsk}, nil
}

// Call 经沙箱执行命令并回流格式化后的输出；危险命令被沙箱拦截时规整为错误结果。
func (t *bashTool) Call(ctx context.Context, input json.RawMessage, _ ProgressSink) (types.ToolResult, error) {
	cmd, err := parseCommand(input)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	ctx, end := t.tracer.Start(ctx, "sandbox.exec", observe.Attr{Key: "command.first", Value: firstWord(cmd)})
	res, execErr := t.sb.Exec(ctx, cmd)
	end(execErr)
	if errors.Is(execErr, sandbox.ErrDangerousCommand) {
		return types.ToolResult{Content: "dangerous command blocked", IsError: true}, nil
	}
	if errors.Is(execErr, sandbox.ErrControlPlaneCommand) {
		return types.ToolResult{Content: "command targets control-plane path, blocked", IsError: true}, nil
	}
	if execErr != nil {
		return types.ToolResult{Content: fmt.Sprintf("sandbox exec: %v", execErr), IsError: true}, nil
	}
	return formatExecResult(res), nil
}

// formatExecResult 把沙箱执行结果合并为回流文本：非零退出码标记为错误并附带退出码。
func formatExecResult(res sandbox.ExecResult) types.ToolResult {
	out := truncate(mergeOutput(res.Stdout, res.Stderr), maxBashOutput)
	if res.ExitCode != 0 {
		if out == "" {
			out = "[no output]"
		}
		return types.ToolResult{Content: fmt.Sprintf("%s\n[exit code: %d]", out, res.ExitCode), IsError: true}
	}
	if out == "" {
		out = "[no output]"
	}
	return types.ToolResult{Content: out}
}

// mergeOutput 合并 stdout 与 stderr，stderr 以分节标注便于模型区分。
func mergeOutput(stdout, stderr string) string {
	var sb strings.Builder
	sb.WriteString(stdout)
	if strings.TrimSpace(stderr) != "" {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("[stderr]\n")
		sb.WriteString(stderr)
	}
	return sb.String()
}

// firstWord 返回命令的首个空白分隔词，用作 span 属性。
func firstWord(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// parseCommand 从入参解析出 command 字段。
func parseCommand(input json.RawMessage) (string, error) {
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	if in.Command == "" {
		return "", errors.New("empty command")
	}
	return in.Command, nil
}

// truncate 把过长输出截断并加省略标记。
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n... [truncated]"
}
