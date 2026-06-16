// Package tool 中的 bash.go 实现 bash 工具的最小安全版：默认 ask + 危险命令拦截 + 工作目录 + 超时。
// 完整的命令沙箱（受限环境 + 执行后清理 + 隔离）留待 Phase 3 在 internal/sandbox 补全。
package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/sandbox"
	"github.com/alaindong/cogent/internal/types"
)

// bash 执行约束常量。
const (
	defaultBashTimeout = 30 * time.Second // 单条命令执行超时
	maxBashOutput      = 32 * 1024        // 回流输出上限，防止撑爆上下文
)

// bashTool 在工作目录内执行 shell 命令；默认非只读、权限 ask、危险命令直接拒绝。
type bashTool struct {
	Defaults
	workRoot string
}

// NewBash 构造 bash 工具。
func NewBash(workRoot string) Tool { return &bashTool{workRoot: workRoot} }

func (t *bashTool) Name() string { return "bash" }
func (t *bashTool) Description() string {
	return "Run a shell command in the workspace root and return its output."
}

func (t *bashTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"shell command to execute"}},"required":["command"]}`)
}

// CheckPermission 危险命令直接拒绝，其余走默认 ask。
func (t *bashTool) CheckPermission(_ context.Context, input json.RawMessage) (permission.Decision, error) {
	cmd, err := parseCommand(input)
	if err != nil {
		return permission.Decision{Behavior: permission.BehaviorDeny, Reason: "invalid command input"}, nil
	}
	if sandbox.IsDangerousCommand(cmd) {
		return permission.Decision{Behavior: permission.BehaviorDeny, Reason: "dangerous command blocked"}, nil
	}
	return permission.Decision{Behavior: permission.BehaviorAsk}, nil
}

// Call 在工作目录内带超时执行命令，回流截断后的合并输出。
func (t *bashTool) Call(ctx context.Context, input json.RawMessage, _ ProgressSink) (types.ToolResult, error) {
	cmd, err := parseCommand(input)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	if sandbox.IsDangerousCommand(cmd) {
		return types.ToolResult{Content: "dangerous command blocked", IsError: true}, nil
	}
	tctx, cancel := context.WithTimeout(ctx, defaultBashTimeout)
	defer cancel()

	var buf bytes.Buffer
	c := exec.CommandContext(tctx, "bash", "-c", cmd)
	c.Dir = t.workRoot
	c.Stdout = &buf
	c.Stderr = &buf
	runErr := c.Run()
	out := truncate(buf.String(), maxBashOutput)
	if runErr != nil {
		return types.ToolResult{Content: fmt.Sprintf("%s\n[exit error: %v]", out, runErr), IsError: true}, nil
	}
	if out == "" {
		out = "[no output]"
	}
	return types.ToolResult{Content: out}, nil
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
		return "", fmt.Errorf("empty command")
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
