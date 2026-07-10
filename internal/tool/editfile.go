// Package tool 中的 editfile.go 实现 edit_file 工具（按旧串精确替换，默认 ask）。
// 支持两种入参形态：单处替换（old_string/new_string）与批量原子替换（edits 数组，
// 按顺序在内存里逐条应用，任一条失败则整体不落盘）——后者用于减少多处修改时的工具
// 调用轮次消耗（P1 效率增强）。
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/sandbox"
	"github.com/alaindong/cogent/internal/types"
)

// editFileTool 在工作目录内的文件中以旧串替换为新串（单处或批量）；默认非只读、权限 ask。
type editFileTool struct {
	Defaults
	workRoot string
}

// NewEditFile 构造 edit_file 工具。
func NewEditFile(workRoot string) Tool { return &editFileTool{workRoot: workRoot} }

func (t *editFileTool) Name() string { return "edit_file" }
func (t *editFileTool) Description() string {
	return "Replace exact substring(s) in a file. Provide old_string/new_string for a single " +
		"replacement, or edits (array of {old_string,new_string}) to apply multiple ordered " +
		"replacements atomically in one call. Each old_string must occur exactly once in the " +
		"content at the moment it is applied; if any edit fails, nothing is written to disk."
}

// editOp 是一条待应用的替换操作。
type editOp struct {
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

// editFileInput 是 edit_file 的入参：legacy 单处字段与批量 edits 二者兼容。
type editFileInput struct {
	Path      string   `json:"path"`
	OldString string   `json:"old_string,omitempty"`
	NewString string   `json:"new_string,omitempty"`
	Edits     []editOp `json:"edits,omitempty"`
}

func (t *editFileTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"path":{"type":"string","description":"file path relative to the workspace root"},` +
		`"old_string":{"type":"string","description":"exact text to replace; must occur exactly once (use this for a single edit, or use edits for multiple)"},` +
		`"new_string":{"type":"string","description":"replacement text (paired with old_string)"},` +
		`"edits":{"type":"array","description":"optional: apply multiple ordered replacements atomically in one call instead of old_string/new_string; each edit's old_string must occur exactly once at the time it is applied, and if any edit fails the whole call fails without writing the file","items":{"type":"object","properties":{"old_string":{"type":"string"},"new_string":{"type":"string"}},"required":["old_string","new_string"]}}` +
		`},"required":["path"]}`)
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

// Call 读取文件、按顺序原子应用一条或多条替换后写回；任一条校验失败则整体不落盘。
func (t *editFileTool) Call(_ context.Context, input json.RawMessage, _ ProgressSink) (types.ToolResult, error) {
	var in editFileInput
	if err := json.Unmarshal(input, &in); err != nil {
		return types.ToolResult{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	edits := in.Edits
	if len(edits) == 0 {
		edits = []editOp{{OldString: in.OldString, NewString: in.NewString}}
	}
	if err := validateEdits(edits); err != nil {
		return types.ToolResult{Content: err.Error(), IsError: true}, nil
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
	updated, err := applyEdits(string(data), edits)
	if err != nil {
		return types.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil {
		return types.ToolResult{Content: fmt.Sprintf("write %s: %v", in.Path, err), IsError: true}, nil
	}
	msg := fmt.Sprintf("edited %s", in.Path)
	if len(edits) > 1 {
		msg = fmt.Sprintf("applied %d edits to %s", len(edits), in.Path)
	}
	return types.ToolResult{
		Content: msg,
		Diff:    unifiedDiff(in.Path, string(data), updated),
	}, nil
}

// validateEdits 校验每条编辑的 old_string 非空；单条时报错文本与历史版本保持一致，
// 批量时附带下标以便模型定位是哪一条出了问题。
func validateEdits(edits []editOp) error {
	for i, e := range edits {
		if e.OldString == "" {
			if len(edits) == 1 {
				return errors.New("old_string must not be empty")
			}
			return fmt.Errorf("edits[%d].old_string must not be empty", i)
		}
	}
	return nil
}

// applyEdits 在 content 上按顺序原子应用每条编辑：后一条可以匹配前一条产生的文本；
// 任一条的 old_string 在应用那一刻不是精确唯一匹配即立即失败并返回错误（不返回部分应用结果），
// 由调用方保证失败时不写回磁盘，避免文件落入中间态。
func applyEdits(content string, edits []editOp) (string, error) {
	cur := content
	for i, e := range edits {
		n := strings.Count(cur, e.OldString)
		if n != 1 {
			if len(edits) == 1 {
				return "", fmt.Errorf("old_string occurs %d times, expected exactly 1", n)
			}
			return "", fmt.Errorf("edits[%d]: old_string occurs %d times, expected exactly 1", i, n)
		}
		cur = strings.Replace(cur, e.OldString, e.NewString, 1)
	}
	return cur, nil
}
