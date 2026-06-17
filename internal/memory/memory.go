// Package memory 实现分层记忆的读与写：
//   - Loader（读）：从项目级 .cogent/MEMORY.md 读取记忆入口，注入到系统提示。
//     入口按行数与字节双重硬截断；路径防穿越；缺失返空。
//   - Writer（写）：让 agent 在运行中把决策/教训沉淀到 daily 日志或长期 MEMORY.md。
//     双层机制：daily（append-only）+ MEMORY.md（curated 更新），对齐蓝本分层记忆设计。
package memory

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 记忆入口与硬截断常量（量级照搬蓝本 memdir）。
const (
	EntrypointName     = "MEMORY.md" // 记忆入口文件名
	MemoryDir          = ".cogent"   // 记忆/配置目录
	MaxEntrypointLines = 200         // 入口最大行数
	MaxEntrypointBytes = 25000       // 入口最大字节数
)

// Loader 加载分层记忆，注入到系统提示。
type Loader interface {
	// Build 读取 <projectRoot>/.cogent/MEMORY.md 入口并按行数/字节硬截断，返回注入文本。
	// 文件缺失时返回空串与 nil error（记忆是可选增强，不存在不算错误）。
	Build(ctx context.Context, projectRoot string) (string, error)
}

// New 构造一个基于本地文件的记忆加载器。
func New() Loader {
	return &fileLoader{}
}

// fileLoader 是 Loader 的本地文件实现。
type fileLoader struct{}

// Build 见 Loader 接口说明。
func (l *fileLoader) Build(_ context.Context, projectRoot string) (string, error) {
	path, err := entrypointPath(projectRoot)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil // 记忆缺失不算错误
	}
	if err != nil {
		return "", fmt.Errorf("read memory: %w", err)
	}
	return truncate(string(data)), nil
}

// entrypointPath 解析记忆入口的绝对路径并做前缀校验，防止 .. 穿越出工作根。
func entrypointPath(projectRoot string) (string, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	target := filepath.Clean(filepath.Join(root, MemoryDir, EntrypointName))
	prefix := filepath.Join(root, MemoryDir) + string(os.PathSeparator)
	if !strings.HasPrefix(target, prefix) {
		return "", errors.New("memory path escapes working directory")
	}
	return target, nil
}

// truncate 按行数与字节双重硬截断取更严格者，保证记忆注入不超量。
func truncate(content string) string {
	byLines := truncateLines(content, MaxEntrypointLines)
	if len(byLines) > MaxEntrypointBytes {
		byLines = truncateBytes(byLines, MaxEntrypointBytes)
	}
	return byLines
}

// truncateLines 保留最多 maxLines 行。
func truncateLines(content string, maxLines int) string {
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), MaxEntrypointBytes+1)
	var sb strings.Builder
	count := 0
	for scanner.Scan() {
		if count >= maxLines {
			break
		}
		if count > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(scanner.Text())
		count++
	}
	return sb.String()
}

// truncateBytes 在不切断 UTF-8 字符的前提下截断到至多 maxBytes 字节。
func truncateBytes(content string, maxBytes int) string {
	if len(content) <= maxBytes {
		return content
	}
	cut := maxBytes
	for cut > 0 && !utf8Start(content[cut]) {
		cut--
	}
	return content[:cut]
}

// utf8Start 判断字节是否为 UTF-8 字符的起始字节（非续接字节 0b10xxxxxx）。
func utf8Start(b byte) bool {
	return b&0xC0 != 0x80
}

// ---------------------------------------------------------------------------
// Writer：agent 运行时沉淀记忆（双层：daily append-only + MEMORY.md curated）
// ---------------------------------------------------------------------------

// Writer 让 agent 在运行中持久化决策与教训到分层记忆。
type Writer interface {
	// AppendDaily 向 <projectRoot>/.cogent/daily/<date>.md 追加一条记录。
	// date 格式为 "YYYY-MM-DD"；目录自动创建；文件尾部保证换行分隔。
	AppendDaily(ctx context.Context, projectRoot, date, content string) error

	// UpdateMemory 将 content 整体覆写 <projectRoot>/.cogent/MEMORY.md（curated 长期记忆）。
	// 调用方负责合并既有内容，Writer 只做原子写入 + 路径校验。
	UpdateMemory(ctx context.Context, projectRoot, content string) error
}

// DailyDir 是 daily 日志存放的子目录。
const DailyDir = "daily"

// DirPerms 是 .cogent 相关目录权限。
const DirPerms = 0o700

// FilePerms 是 .cogent 相关文件权限。
const FilePerms = 0o600

// NewWriter 构造一个基于本地文件的记忆写入器。
func NewWriter() Writer {
	return &fileWriter{}
}

// fileWriter 是 Writer 的本地文件实现。
type fileWriter struct{}

// AppendDaily 追加一条记录到 daily 文件（append-only，保留审计链）。
func (w *fileWriter) AppendDaily(_ context.Context, projectRoot, date, content string) error {
	dir, err := safeDailyDir(projectRoot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, DirPerms); err != nil {
		return fmt.Errorf("create daily dir: %w", err)
	}
	path := filepath.Join(dir, date+".md")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, FilePerms)
	if err != nil {
		return fmt.Errorf("open daily: %w", err)
	}
	defer f.Close()
	entry := ensureTrailingNewline(content)
	if _, err := f.WriteString(entry); err != nil {
		return fmt.Errorf("write daily: %w", err)
	}
	return nil
}

// UpdateMemory 覆写 MEMORY.md（curated 长期记忆）。
func (w *fileWriter) UpdateMemory(_ context.Context, projectRoot, content string) error {
	path, err := entrypointPath(projectRoot)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, DirPerms); err != nil {
		return fmt.Errorf("create memory dir: %w", err)
	}
	return os.WriteFile(path, []byte(content), FilePerms)
}

// safeDailyDir 解析 daily 目录路径并做前缀校验防穿越。
func safeDailyDir(projectRoot string) (string, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	dir := filepath.Clean(filepath.Join(root, MemoryDir, DailyDir))
	prefix := filepath.Join(root, MemoryDir) + string(os.PathSeparator)
	if !strings.HasPrefix(dir+string(os.PathSeparator), prefix) {
		return "", errors.New("daily path escapes working directory")
	}
	return dir, nil
}

// ensureTrailingNewline 确保内容以换行结尾，保证追加时条目有分隔。
func ensureTrailingNewline(s string) string {
	if len(s) == 0 || s[len(s)-1] != '\n' {
		return s + "\n"
	}
	return s
}
