// Package tool 中的 readfile.go 实现只读的 read_file 工具。
package tool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/sandbox"
	"github.com/alaindong/cogent/internal/types"
)

// maxReadBytes 是单次读取回流的上限，防止超大文件撑爆上下文。
const maxReadBytes = 64 * 1024

// maxReadLines 是范围读取（offset/limit）时的行数硬上限，防止 limit 传入超大值撑爆上下文；
// 传入值超过该上限时按上限截断，而非报错，便于模型分批翻页读完整个大文件。
const maxReadLines = 2000

// readFileTool 读取工作目录内的文件内容（只读、并发安全）。
type readFileTool struct {
	Defaults
	workRoot string
}

// NewReadFile 构造 read_file 工具。
func NewReadFile(workRoot string) Tool { return &readFileTool{workRoot: workRoot} }

func (t *readFileTool) Name() string { return "read_file" }
func (t *readFileTool) Description() string {
	return "Read the contents of a file within the workspace. Without offset/limit this reads " +
		"the whole file (capped at 64KB). Pass offset (1-based start line) and/or limit " +
		"(max lines) to stream a specific line range from large files that exceed the cap."
}

func (t *readFileTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"path":{"type":"string","description":"file path relative to the workspace root"},` +
		`"offset":{"type":"integer","description":"optional: 1-based line number to start reading from; omit to read from the beginning"},` +
		`"limit":{"type":"integer","description":"optional: max number of lines to read starting at offset; omit for the legacy full-file read (subject to a 64KB size cap)"}` +
		`},"required":["path"]}`)
}

// IsReadOnly 只读。
func (t *readFileTool) IsReadOnly(json.RawMessage) bool { return true }

// IsConcurrencySafe 只读可并发。
func (t *readFileTool) IsConcurrencySafe(json.RawMessage) bool { return true }

// CheckPermission 只读操作直接放行。
func (t *readFileTool) CheckPermission(context.Context, json.RawMessage) (permission.Decision, error) {
	return permission.Decision{Behavior: permission.BehaviorAllow}, nil
}

// readFileInput 是 read_file 的入参：offset/limit 均为可选，缺省时走 legacy 全文读。
type readFileInput struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// Call 读取并回流文件内容；传入 offset/limit 时改走流式的行范围读取。
func (t *readFileTool) Call(_ context.Context, input json.RawMessage, _ ProgressSink) (types.ToolResult, error) {
	var in readFileInput
	if err := json.Unmarshal(input, &in); err != nil {
		return types.ToolResult{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	abs, err := sandbox.ValidatePath(t.workRoot, in.Path)
	if err != nil {
		return types.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	if in.Offset > 0 || in.Limit > 0 {
		return readRange(abs, in.Path, in.Offset, in.Limit)
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

// readRange 从 abs 按 1-based offset 起始、至多 limit 行流式读取（bufio.Scanner 逐行扫描，
// 不整体载入内存），用于绕开全文读 64KB 截断对大文件中段/尾段不可达的限制；同时叠加
// maxReadBytes 字节上限兜底，防止超大 limit 撑爆回流内容。
func readRange(abs, relPath string, offset, limit int) (types.ToolResult, error) {
	if offset <= 0 {
		offset = 1
	}
	if limit <= 0 || limit > maxReadLines {
		limit = maxReadLines
	}
	f, err := os.Open(abs)
	if err != nil {
		return types.ToolResult{Content: fmt.Sprintf("read %s: %v", relPath, err), IsError: true}, nil
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	content, collected, truncated := scanLines(scanner, offset, limit)
	if collected == 0 {
		return types.ToolResult{
			Content: fmt.Sprintf("no lines in range [offset=%d, limit=%d] for %s", offset, limit, relPath),
		}, nil
	}
	suffix := fmt.Sprintf("\n[showing lines %d-%d]", offset, offset+collected-1)
	if truncated {
		suffix = fmt.Sprintf("\n[showing lines %d-%d, truncated by size]", offset, offset+collected-1)
	}
	return types.ToolResult{Content: content + suffix}, nil
}

// scanLines 跳过前 offset-1 行后收集至多 limit 行，累计字节超过 maxReadBytes 时提前停止
// （truncated=true）。返回收集到的文本（每行以 \n 结尾）与实际收集行数。
func scanLines(scanner *bufio.Scanner, offset, limit int) (content string, collected int, truncated bool) {
	var sb strings.Builder
	lineNo, size := 0, 0
	for scanner.Scan() {
		lineNo++
		if lineNo < offset {
			continue
		}
		if collected >= limit {
			break
		}
		text := scanner.Text()
		size += len(text) + 1
		if size > maxReadBytes {
			truncated = true
			break
		}
		sb.WriteString(text)
		sb.WriteByte('\n')
		collected++
	}
	return sb.String(), collected, truncated
}
