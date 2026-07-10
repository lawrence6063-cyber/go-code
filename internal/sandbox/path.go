// 本文件实现 sandbox 的纯函数防线：路径越界校验、控制面写禁止、危险命令识别，均可独立单测。
// 包级文档见 doc.go。
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
// 仅收录明确高危且极少出现在合法工作区操作中的片段，避免误拦开发者正常命令（如 rm -rf ./build）。
var dangerousFragments = []string{
	"rm -rf /",
	"rm -rf /*",
	"rm -rf ~",
	"mkfs",
	"dd if=",
	"> /dev/sd",
	"> /dev/sda",
	"chmod -r 777 /",  // 递归放开根权限
	"chown -r root /", // 递归改根属主
}

// ValidatePath 校验 target 解析后仍位于 workRoot 之内，返回清理后的绝对路径，否则返回 ErrPathEscape。
// 在 Clean + 字符串前缀校验之上，进一步对真实落点解析符号链接（EvalSymlinks）后再次校验，
// 堵住"工作区内 symlink 指向区外"的越界缺口（OPTIMIZE_SPEC S1）；目标不存在时解析其最近的已存在父目录。
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
	// 解析软链接后再校验真实落点：root 本身也先解析（如 macOS /tmp → /private/tmp），避免前缀比对误判。
	realRoot := evalSymlinks(root)
	realTarget := evalSymlinks(abs)
	if realTarget != realRoot && !strings.HasPrefix(realTarget, realRoot+string(os.PathSeparator)) {
		return "", ErrPathEscape
	}
	return abs, nil
}

// evalSymlinks 解析 path 的符号链接得到真实路径；path 不存在时回退到其最近已存在父目录的真实路径，
// 仍无法解析时返回 Clean 后的原路径（保守兜底，宁可后续前缀校验拒绝也不放过越界）。
func evalSymlinks(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	dir := path
	for {
		parent := filepath.Dir(dir)
		if parent == dir { // 触达根，无法再上溯
			return filepath.Clean(path)
		}
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			// 父目录真实路径 + 剩余未解析的相对部分。
			rel, rerr := filepath.Rel(parent, path)
			if rerr != nil {
				return resolved
			}
			return filepath.Join(resolved, rel)
		}
		dir = parent
	}
}

// controlPlaneCommandVerbs 是常见的会修改文件系统的命令首词，其非 flag 参数需要过控制面校验。
var controlPlaneCommandVerbs = map[string]bool{
	"rm": true, "mv": true, "cp": true, "touch": true, "mkdir": true, "tee": true, "sed": true,
}

// IsControlPlaneCommandTarget 报告 command（含复合命令的任一子命令）是否会写入/修改受保护的
// 控制面路径（.cogent/.git）。这是启发式的 token 级判定，不做完整 shell 语法解析——只识别
// 两类高频模式：① 输出重定向（>/>>）后的路径 token；② 常见改动文件系统的命令
// （rm/mv/cp/touch/mkdir/tee/sed）的非 flag 参数。与 dangerousFragments 同一哲学：
// 宁可误拦（把非路径 token 也过一遍校验）不可漏放，堵住 write_file/edit_file 已拦截、
// 但 bash 尚未拦截的控制面写入绕过缺口。
func IsControlPlaneCommandTarget(workRoot, command string) bool {
	for _, sub := range splitComposite(command) {
		for _, target := range candidateWriteTargets(sub) {
			if IsControlPlaneWrite(workRoot, target) {
				return true
			}
		}
	}
	return false
}

// candidateWriteTargets 从单条子命令中提取可能的写入目标 token：重定向目标 + 改动类命令的
// 非 flag 参数。允许把非路径的普通参数也当作候选一并校验，误判后果只是多算一次
// IsControlPlaneWrite（几乎总是 false），不会造成漏放。
func candidateWriteTargets(sub string) []string {
	fields := strings.Fields(sub)
	if len(fields) == 0 {
		return nil
	}
	var out []string
	for i, f := range fields {
		switch {
		case f == ">" || f == ">>":
			if i+1 < len(fields) {
				out = append(out, fields[i+1])
			}
		case strings.HasPrefix(f, ">>") && len(f) > 2:
			out = append(out, f[2:])
		case strings.HasPrefix(f, ">") && len(f) > 1:
			out = append(out, f[1:])
		}
	}
	if controlPlaneCommandVerbs[fields[0]] {
		for _, f := range fields[1:] {
			if f == "" || strings.HasPrefix(f, "-") {
				continue // 跳过 flag，如 -rf/-i
			}
			out = append(out, f)
		}
	}
	return out
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

// splitComposite 按 shell 复合分隔符（&& || ; 换行 管道）拆分命令为子命令序列；
// 管道 "|" 必须在 "||" 之后替换，避免把 "||" 误拆成两段。拆分越细，dangerousFragments/
// IsControlPlaneWrite 等子串级检测越不会漏放（如 "echo x | tee .git/config" 需要拆出
// "tee .git/config" 才能命中改动类命令识别），对既有检测语义只会更严格、不会更宽松。
func splitComposite(command string) []string {
	repl := command
	for _, sep := range []string{"&&", "||", ";", "\n", "|"} {
		repl = strings.ReplaceAll(repl, sep, "\x00")
	}
	return strings.Split(repl, "\x00")
}

// isPipeToShell 识别"远程下载或编码内容直接管道给 shell 执行"这类高危模式
// （curl/wget ... | sh|bash，或 base64 -d ... | sh|bash 的编码绕过变体）。
func isPipeToShell(command string) bool {
	lower := strings.ToLower(command)
	if !strings.Contains(lower, "|") {
		return false
	}
	hasFetch := strings.Contains(lower, "curl") || strings.Contains(lower, "wget")
	hasDecode := strings.Contains(lower, "base64 -d") || strings.Contains(lower, "base64 --decode")
	if !hasFetch && !hasDecode {
		return false
	}
	segments := strings.Split(lower, "|")
	last := strings.TrimSpace(segments[len(segments)-1])
	return strings.HasPrefix(last, "sh") || strings.HasPrefix(last, "bash")
}
