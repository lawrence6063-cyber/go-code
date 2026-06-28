// Package tool 中的 findfiles.go 实现只读的 find_files 工具（工作目录内按 glob 模式递归查找文件名）。
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/sandbox"
	"github.com/alaindong/cogent/internal/types"
)

// maxFindFiles 是回流文件路径的上限，防止超大仓库撑爆上下文。
const maxFindFiles = 200

// findFilesTool 在工作目录内按 glob 模式递归查找文件名（只读、并发安全）。
type findFilesTool struct {
	Defaults
	workRoot string
}

// NewFindFiles 构造 find_files 工具。
func NewFindFiles(workRoot string) Tool { return &findFilesTool{workRoot: workRoot} }

func (t *findFilesTool) Name() string { return "find_files" }
func (t *findFilesTool) Description() string {
	return "Find files by glob pattern within the workspace (recursive)."
}

func (t *findFilesTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"glob pattern to match file base name (e.g. *.go, test_*) using Go filepath.Match semantics"},"path":{"type":"string","description":"directory to start search from, relative to workspace root; empty means workspace root"}},"required":["pattern"]}`)
}

// IsReadOnly 只读。
func (t *findFilesTool) IsReadOnly(json.RawMessage) bool { return true }

// IsConcurrencySafe 只读可并发。
func (t *findFilesTool) IsConcurrencySafe(json.RawMessage) bool { return true }

// CheckPermission 只读操作直接放行。
func (t *findFilesTool) CheckPermission(context.Context, json.RawMessage) (permission.Decision, error) {
	return permission.Decision{Behavior: permission.BehaviorAllow}, nil
}

// Call 遍历检索根按 glob 查找文件名并回流匹配路径（每行一个，字典序）。
func (t *findFilesTool) Call(ctx context.Context, input json.RawMessage, _ ProgressSink) (types.ToolResult, error) {
	var in struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return types.ToolResult{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	if in.Pattern == "" {
		return types.ToolResult{Content: "pattern is required", IsError: true}, nil
	}
	if in.Path == "" {
		in.Path = "."
	}
	root, err := sandbox.ValidatePath(t.workRoot, in.Path)
	if err != nil {
		return types.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	matches := findTree(ctx, root, t.workRoot, in.Pattern)
	if len(matches) == 0 {
		return types.ToolResult{Content: fmt.Sprintf("no files found matching %q", in.Pattern)}, nil
	}
	return types.ToolResult{Content: strings.Join(matches, "\n")}, nil
}

// findTree 遍历 root 下所有文件，按 pattern（Go filepath.Match 语义）匹配文件名 base name，
// 返回相对 workRoot 的路径列表（上限 maxFindFiles），按字典序排序。
func findTree(ctx context.Context, root, workRoot, pattern string) []string {
	var matches []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			if ctx.Err() != nil {
				return filepath.SkipAll
			}
			return nil
		}
		if d.IsDir() {
			if skippedDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if len(matches) >= maxFindFiles {
			return filepath.SkipAll
		}
		matched, err := filepath.Match(pattern, d.Name())
		if err != nil {
			return nil // 无效 pattern 静默跳过（Call 已校验，此处不会触发）
		}
		if matched {
			rel, _ := filepath.Rel(workRoot, path)
			matches = append(matches, rel)
		}
		return nil
	})
	sort.Strings(matches)
	if len(matches) > maxFindFiles {
		matches = matches[:maxFindFiles]
	}
	return matches
}

// Ensure findFilesTool implements Tool at compile time.
var _ Tool = (*findFilesTool)(nil)
