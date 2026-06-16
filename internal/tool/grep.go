// Package tool 中的 grep.go 实现只读的 grep 工具（工作目录内正则文本检索）。
package tool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/sandbox"
	"github.com/alaindong/cogent/internal/types"
)

// maxGrepMatches 是回流匹配行的上限，防止结果过大。
const maxGrepMatches = 200

// skippedDirs 是检索时跳过的目录（噪声/控制面）。
var skippedDirs = map[string]bool{".git": true, ".cogent": true, "node_modules": true}

// grepTool 在工作目录内按正则检索文本（只读、并发安全）。
type grepTool struct {
	Defaults
	workRoot string
}

// NewGrep 构造 grep 工具。
func NewGrep(workRoot string) Tool { return &grepTool{workRoot: workRoot} }

func (t *grepTool) Name() string { return "grep" }
func (t *grepTool) Description() string {
	return "Search file contents by regular expression within the workspace."
}

func (t *grepTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"regular expression"},"path":{"type":"string","description":"directory or file to search; empty means workspace root"}},"required":["pattern"]}`)
}

// IsReadOnly 只读。
func (t *grepTool) IsReadOnly(json.RawMessage) bool { return true }

// IsConcurrencySafe 只读可并发。
func (t *grepTool) IsConcurrencySafe(json.RawMessage) bool { return true }

// CheckPermission 只读操作直接放行。
func (t *grepTool) CheckPermission(context.Context, json.RawMessage) (permission.Decision, error) {
	return permission.Decision{Behavior: permission.BehaviorAllow}, nil
}

// Call 遍历检索根并回流命中行（formatted: 相对路径:行号:内容）。
func (t *grepTool) Call(ctx context.Context, input json.RawMessage, _ ProgressSink) (types.ToolResult, error) {
	var in struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return types.ToolResult{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	re, err := regexp.Compile(in.Pattern)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("invalid pattern: %v", err), IsError: true}, nil
	}
	if in.Path == "" {
		in.Path = "."
	}
	root, err := sandbox.ValidatePath(t.workRoot, in.Path)
	if err != nil {
		return types.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	matches := searchTree(ctx, root, t.workRoot, re)
	if len(matches) == 0 {
		return types.ToolResult{Content: "no matches"}, nil
	}
	return types.ToolResult{Content: strings.Join(matches, "\n")}, nil
}

// searchTree 遍历 root 下文件按 re 检索，返回格式化命中行（上限 maxGrepMatches）。
func searchTree(ctx context.Context, root, workRoot string, re *regexp.Regexp) []string {
	var matches []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil || len(matches) >= maxGrepMatches {
			if len(matches) >= maxGrepMatches || ctx.Err() != nil {
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
		matches = append(matches, scanFile(path, workRoot, re, maxGrepMatches-len(matches))...)
		return nil
	})
	return matches
}

// scanFile 逐行检索单个文件，最多返回 limit 条命中（相对路径:行号:内容）。
func scanFile(path, workRoot string, re *regexp.Regexp, limit int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	rel, _ := filepath.Rel(workRoot, path)
	var out []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; scanner.Scan() && len(out) < limit; line++ {
		if re.MatchString(scanner.Text()) {
			out = append(out, fmt.Sprintf("%s:%d:%s", rel, line, scanner.Text()))
		}
	}
	return out
}
