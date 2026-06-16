// Package tool 中的 writefile.go 实现 write_file 工具（默认 ask，控制面写禁止）。
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/sandbox"
	"github.com/alaindong/cogent/internal/types"
)

// writeFileTool 写入（创建/覆盖）工作目录内的文件；默认非只读、权限 ask。
type writeFileTool struct {
	Defaults
	workRoot string
}

// NewWriteFile 构造 write_file 工具。
func NewWriteFile(workRoot string) Tool { return &writeFileTool{workRoot: workRoot} }

func (t *writeFileTool) Name() string { return "write_file" }
func (t *writeFileTool) Description() string {
	return "Create or overwrite a file within the workspace with the given content."
}

func (t *writeFileTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"file path relative to the workspace root"},"content":{"type":"string","description":"full file content to write"}},"required":["path","content"]}`)
}

// CheckPermission 控制面写入直接拒绝，其余走默认 ask。
func (t *writeFileTool) CheckPermission(_ context.Context, input json.RawMessage) (permission.Decision, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &in); err == nil && sandbox.IsControlPlaneWrite(t.workRoot, in.Path) {
		return permission.Decision{Behavior: permission.BehaviorDeny, Reason: "write to control-plane path is forbidden"}, nil
	}
	return permission.Decision{Behavior: permission.BehaviorAsk}, nil
}

// Call 校验路径与控制面后写入文件。
func (t *writeFileTool) Call(_ context.Context, input json.RawMessage, _ ProgressSink) (types.ToolResult, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return types.ToolResult{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	if sandbox.IsControlPlaneWrite(t.workRoot, in.Path) {
		return types.ToolResult{Content: "write to control-plane path is forbidden", IsError: true}, nil
	}
	abs, err := sandbox.ValidatePath(t.workRoot, in.Path)
	if err != nil {
		return types.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return types.ToolResult{Content: fmt.Sprintf("mkdir: %v", err), IsError: true}, nil
	}
	if err := os.WriteFile(abs, []byte(in.Content), 0o644); err != nil {
		return types.ToolResult{Content: fmt.Sprintf("write %s: %v", in.Path, err), IsError: true}, nil
	}
	return types.ToolResult{Content: fmt.Sprintf("wrote %d bytes to %s", len(in.Content), in.Path)}, nil
}
