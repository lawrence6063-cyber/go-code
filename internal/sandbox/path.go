// Package sandbox 约束命令执行与文件操作的安全边界。
// Phase 2 先落地路径越界校验、控制面写禁止与危险命令识别（均为纯函数，可独立单测）；
// 完整的命令隔离（路由 + 受限执行 + 执行后清理）留待 Phase 3 在同包补全。
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrPathEscape 表示目标路径越出了允许的工作根目录。
var ErrPathEscape = errors.New("path escapes working directory")

// controlPlanePrefixes 是禁止写入的控制面路径（相对 workRoot），防止"注入→持久化→长期控制"。
var controlPlanePrefixes = []string{".cogent", ".git"}

// dangerousFragments 是保守拦截的破坏性命令片段（小写子串匹配，宁可误拦不可漏放）。
var dangerousFragments = []string{
	"rm -rf /",
	"rm -rf /*",
	"rm -rf ~",
	"mkfs",
	"dd if=",
	"> /dev/sd",
}

// ValidatePath 校验 target 解析后仍位于 workRoot 之内，返回清理后的绝对路径，否则返回 ErrPathEscape。
func ValidatePath(workRoot, target string) (string, error) {
	root, err := filepath.Abs(workRoot)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	abs := target
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, target)
	}
	abs = filepath.Clean(abs)
	if abs != root && !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		return "", ErrPathEscape
	}
	return abs, nil
}

// IsControlPlaneWrite 报告对 target 的写入是否触碰受保护的控制面（含越界，越界一律视为禁止）。
func IsControlPlaneWrite(workRoot, target string) bool {
	abs, err := ValidatePath(workRoot, target)
	if err != nil {
		return true
	}
	root, err := filepath.Abs(workRoot)
	if err != nil {
		return true
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return true
	}
	for _, p := range controlPlanePrefixes {
		if rel == p || strings.HasPrefix(rel, p+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// IsDangerousCommand 报告命令（含复合命令的任一子命令）是否命中危险模式——任一子命令危险即整体危险。
func IsDangerousCommand(command string) bool {
	// fork bomb 含分隔符，需在整命令（去空白）上整体匹配，不能依赖复合拆分。
	if strings.Contains(strings.ReplaceAll(strings.ToLower(command), " ", ""), ":(){:|:&};:") {
		return true
	}
	for _, sub := range splitComposite(command) {
		s := strings.ToLower(strings.TrimSpace(sub))
		if s == "" {
			continue
		}
		for _, frag := range dangerousFragments {
			if strings.Contains(s, frag) {
				return true
			}
		}
	}
	return isPipeToShell(command)
}

// splitComposite 按 shell 复合分隔符（&& || ; 换行）拆分命令为子命令序列。
func splitComposite(command string) []string {
	repl := command
	for _, sep := range []string{"&&", "||", ";", "\n"} {
		repl = strings.ReplaceAll(repl, sep, "\x00")
	}
	return strings.Split(repl, "\x00")
}

// isPipeToShell 识别"远程下载内容直接管道给 shell 执行"这类高危模式（curl/wget ... | sh|bash）。
func isPipeToShell(command string) bool {
	lower := strings.ToLower(command)
	if !strings.Contains(lower, "|") {
		return false
	}
	hasFetch := strings.Contains(lower, "curl") || strings.Contains(lower, "wget")
	if !hasFetch {
		return false
	}
	segments := strings.Split(lower, "|")
	last := strings.TrimSpace(segments[len(segments)-1])
	return strings.HasPrefix(last, "sh") || strings.HasPrefix(last, "bash")
}
