// Package tool 中的 readfile.go 实现只读的 read_file 工具。
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/sandbox"
	"github.com/alaindong/cogent/internal/types"
)

// maxReadBytes 是单次读取回流的上限，防止超大文件撑爆上下文。
const maxReadBytes = 64 * 1024

// readFileTool 读取工作目录内的文件内容（只读、并发安全）。
type readFileTool struct {
	Defaults
	workRoot string
}

// NewReadFile 构造 read_file 工具。
func NewReadFile(workRoot string) Tool { return &readFileTool{workRoot: workRoot} }

func (t *readFileTool) Name() string { return "read_file" }
func (t *readFileTool) Description() string {
	return "Read the contents of a file within the workspace."
}

func (t *readFileTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"file path relative to the workspace root"}},"required":["path"]}`)
}

// IsReadOnly 只读。
func (t *readFileTool) IsReadOnly(json.RawMessage) bool { return true }

// IsConcurrencySafe 只读可并发。
func (t *readFileTool) IsConcurrencySafe(json.RawMessage) bool { return true }

// CheckPermission 只读操作直接放行。
func (t *readFileTool) CheckPermission(context.Context, json.RawMessage) (permission.Decision, error) {
	return permission.Decision{Behavior: permission.BehaviorAllow}, nil
}

// Call 读取并回流文件内容。
func (t *readFileTool) Call(_ context.Context, input json.RawMessage, _ ProgressSink) (types.ToolResult, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return types.ToolResult{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	abs, err := sandbox.ValidatePath(t.workRoot, in.Path)
	if err != nil {
		return types.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("read %s: %v", in.Path, err), IsError: true}, nil
	}
	content := string(data)
	if len(data) > maxReadBytes {
		content = string(data[:maxReadBytes]) + "\n... [truncated]"
	}
	return types.ToolResult{Content: content}, nil
}
