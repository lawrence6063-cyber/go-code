// Package tool 中的 editfile.go 实现 edit_file 工具（按旧串精确替换，默认 ask）。
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/sandbox"
	"github.com/alaindong/cogent/internal/types"
)

// editFileTool 在工作目录内的文件中以旧串替换为新串；默认非只读、权限 ask。
type editFileTool struct {
	Defaults
	workRoot string
}

// NewEditFile 构造 edit_file 工具。
func NewEditFile(workRoot string) Tool { return &editFileTool{workRoot: workRoot} }

func (t *editFileTool) Name() string { return "edit_file" }
func (t *editFileTool) Description() string {
	return "Replace an exact substring (old_string) with new_string in a file. old_string must occur exactly once."
}

func (t *editFileTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"file path relative to the workspace root"},"old_string":{"type":"string","description":"exact text to replace; must occur exactly once"},"new_string":{"type":"string","description":"replacement text"}},"required":["path","old_string","new_string"]}`)
}

// CheckPermission 控制面写入直接拒绝，其余走默认 ask。
func (t *editFileTool) CheckPermission(_ context.Context, input json.RawMessage) (permission.Decision, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &in); err == nil && sandbox.IsControlPlaneWrite(t.workRoot, in.Path) {
		return permission.Decision{Behavior: permission.BehaviorDeny, Reason: "edit of control-plane path is forbidden"}, nil
	}
	return permission.Decision{Behavior: permission.BehaviorAsk}, nil
}

// Call 读取文件、校验旧串唯一性后替换并写回。
func (t *editFileTool) Call(_ context.Context, input json.RawMessage, _ ProgressSink) (types.ToolResult, error) {
	var in struct {
		Path      string `json:"path"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return types.ToolResult{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	if in.OldString == "" {
		return types.ToolResult{Content: "old_string must not be empty", IsError: true}, nil
	}
	if sandbox.IsControlPlaneWrite(t.workRoot, in.Path) {
		return types.ToolResult{Content: "edit of control-plane path is forbidden", IsError: true}, nil
	}
	abs, err := sandbox.ValidatePath(t.workRoot, in.Path)
	if err != nil {
		return types.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("read %s: %v", in.Path, err), IsError: true}, nil
	}
	if n := strings.Count(string(data), in.OldString); n != 1 {
		return types.ToolResult{Content: fmt.Sprintf("old_string occurs %d times, expected exactly 1", n), IsError: true}, nil
	}
	updated := strings.Replace(string(data), in.OldString, in.NewString, 1)
	if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil {
		return types.ToolResult{Content: fmt.Sprintf("write %s: %v", in.Path, err), IsError: true}, nil
	}
	return types.ToolResult{
		Content: fmt.Sprintf("edited %s", in.Path),
		Diff:    unifiedDiff(in.Path, string(data), updated),
	}, nil
}
