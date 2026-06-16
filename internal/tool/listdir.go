// Package tool 中的 listdir.go 实现只读的 list_dir 工具。
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/sandbox"
	"github.com/alaindong/cogent/internal/types"
)

// listDirTool 列出工作目录内某目录的条目（只读、并发安全）。
type listDirTool struct {
	Defaults
	workRoot string
}

// NewListDir 构造 list_dir 工具。
func NewListDir(workRoot string) Tool { return &listDirTool{workRoot: workRoot} }

func (t *listDirTool) Name() string { return "list_dir" }
func (t *listDirTool) Description() string {
	return "List entries of a directory within the workspace."
}

func (t *listDirTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"directory path relative to the workspace root; empty means root"}}}`)
}

// IsReadOnly 只读。
func (t *listDirTool) IsReadOnly(json.RawMessage) bool { return true }

// IsConcurrencySafe 只读可并发。
func (t *listDirTool) IsConcurrencySafe(json.RawMessage) bool { return true }

// CheckPermission 只读操作直接放行。
func (t *listDirTool) CheckPermission(context.Context, json.RawMessage) (permission.Decision, error) {
	return permission.Decision{Behavior: permission.BehaviorAllow}, nil
}

// Call 列出目录条目，目录项以 "/" 结尾标识。
func (t *listDirTool) Call(_ context.Context, input json.RawMessage, _ ProgressSink) (types.ToolResult, error) {
	var in struct {
		Path string `json:"path"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return types.ToolResult{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
		}
	}
	if in.Path == "" {
		in.Path = "."
	}
	abs, err := sandbox.ValidatePath(t.workRoot, in.Path)
	if err != nil {
		return types.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("list %s: %v", in.Path, err), IsError: true}, nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return types.ToolResult{Content: strings.Join(names, "\n")}, nil
}
