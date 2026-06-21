// 本文件在纯函数防线之上补全命令执行的纵深防御：命令路由（ShouldSandbox）+ 危险命令确定性拦截
// + 受限环境执行 + 工作目录约束 + 超时 + 执行后清理。包级文档见 doc.go。
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrDangerousCommand 表示命令命中危险模式被确定性拦截（无论是否启用沙箱均生效）。
var ErrDangerousCommand = errors.New("dangerous command blocked")

// defaultExecTimeout 是未配置 Timeout 时单条命令的执行超时。
const defaultExecTimeout = 30 * time.Second

// bareRepoSignature 是判定"bare git repo 逃逸伪造"的核心签名条目：三者齐备方视为构成 bare repo。
var bareRepoSignature = []string{"HEAD", "objects", "refs"}

// bareRepoArtifacts 是一个 bare git repo 可能在工作根目录顶层留下的全部伪造产物，清理时仅移除本次新建者。
var bareRepoArtifacts = []string{
	"HEAD", "objects", "refs", "config", "branches",
	"description", "hooks", "info", "packed-refs",
}

// ExecResult 是一次命令执行的结果：标准输出、标准错误与退出码分离呈现。
type ExecResult struct {
	Stdout   string // 标准输出
	Stderr   string // 标准错误
	ExitCode int    // 进程退出码（被信号杀死或启动失败时为 -1）
}

// Config 配置沙箱执行边界；由 cmd 层在装配工具池时构造。
type Config struct {
	WorkRoot         string        // 命令执行的工作根目录，也是受限环境的 HOME
	Timeout          time.Duration // 单条命令超时；<=0 时取默认 30s
	Enabled          bool          // 是否启用受限环境（false 时仍拦截危险命令，但继承宿主环境）
	ExcludedCommands []string      // 豁免受限环境的命令首词（便利豁免，非安全边界）
	DeniedRules      []string      // 追加的危险命令片段（在内建 dangerousFragments 之外）
}

// Sandbox 约束并执行 shell 命令：路由决定是否受限，执行做危险拦截 + 工作目录约束 + 超时 + 执行后清理。
type Sandbox interface {
	// ShouldSandbox 报告某命令是否应在受限环境下执行。
	ShouldSandbox(command string) bool
	// Exec 在工作目录内执行命令；危险命令返回 ErrDangerousCommand，否则返回分离捕获的执行结果。
	Exec(ctx context.Context, command string) (ExecResult, error)
}

// sandbox 是 Sandbox 的默认实现。
type sandbox struct {
	cfg Config
}

// New 构造一个 Sandbox；Timeout<=0 时回退到默认超时。
func New(cfg Config) Sandbox {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultExecTimeout
	}
	return &sandbox{cfg: cfg}
}

// ShouldSandbox 见 Sandbox 接口说明：未启用或命中豁免命令首词时返回 false。
func (s *sandbox) ShouldSandbox(command string) bool {
	if !s.cfg.Enabled {
		return false
	}
	first := firstWord(command)
	for _, ex := range s.cfg.ExcludedCommands {
		if first == ex {
			return false
		}
	}
	return true
}

// Exec 见 Sandbox 接口说明：先做危险拦截，再带超时执行并分离捕获输出，最后清理 bare-repo 逃逸产物。
func (s *sandbox) Exec(ctx context.Context, command string) (ExecResult, error) {
	if s.isDangerous(command) {
		return ExecResult{ExitCode: -1}, ErrDangerousCommand
	}
	tctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	before := s.snapshotTopLevel()
	res := s.runCommand(tctx, command)
	s.scrubBareGitRepoFiles(before)
	return res, nil
}

// isDangerous 综合内建危险模式与配置的 DeniedRules 判定命令是否危险。
func (s *sandbox) isDangerous(command string) bool {
	if IsDangerousCommand(command) {
		return true
	}
	for _, sub := range splitComposite(command) {
		low := strings.ToLower(strings.TrimSpace(sub))
		for _, rule := range s.cfg.DeniedRules {
			if rule != "" && strings.Contains(low, strings.ToLower(rule)) {
				return true
			}
		}
	}
	return false
}

// runCommand 用 bash -c 在工作目录内执行命令，分离捕获 stdout/stderr 并解析退出码。
func (s *sandbox) runCommand(ctx context.Context, command string) ExecResult {
	var stdout, stderr bytes.Buffer
	c := exec.CommandContext(ctx, "bash", "-c", command)
	c.Dir = s.cfg.WorkRoot
	c.Stdout = &stdout
	c.Stderr = &stderr
	if s.ShouldSandbox(command) {
		c.Env = s.restrictedEnv()
	}
	err := c.Run()
	return ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode(err),
	}
}

// restrictedEnv 构造最小受限环境：精简 PATH、HOME 指向 WorkRoot，不透传宿主密钥。
func (s *sandbox) restrictedEnv() []string {
	return []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + s.cfg.WorkRoot,
		"SHELL=/bin/bash",
		"LANG=C.UTF-8",
	}
}

// snapshotTopLevel 记录 WorkRoot 顶层条目名集合，供执行后比对出本次新建的产物。
func (s *sandbox) snapshotTopLevel() map[string]bool {
	set := make(map[string]bool)
	entries, err := os.ReadDir(s.cfg.WorkRoot)
	if err != nil {
		return set
	}
	for _, e := range entries {
		set[e.Name()] = true
	}
	return set
}

// scrubBareGitRepoFiles 清理本次执行新建的 bare git repo 伪造产物：
// 仅当 {HEAD,objects,refs} 签名全部为本次新建时，移除本次新增的 bare-repo 产物；既有 .git 与用户文件绝不触碰。
func (s *sandbox) scrubBareGitRepoFiles(before map[string]bool) {
	if !s.bareRepoFreshlyCreated(before) {
		return
	}
	for _, name := range bareRepoArtifacts {
		if before[name] {
			continue // 既有条目，绝不删除
		}
		path := filepath.Join(s.cfg.WorkRoot, name)
		if isNewlyCreated(path, before, name) {
			_ = os.RemoveAll(path)
		}
	}
}

// bareRepoFreshlyCreated 报告 bare repo 的核心签名条目是否全部为本次执行新建。
func (s *sandbox) bareRepoFreshlyCreated(before map[string]bool) bool {
	for _, name := range bareRepoSignature {
		if before[name] {
			return false // 已存在，说明不是本次伪造
		}
		if _, err := os.Stat(filepath.Join(s.cfg.WorkRoot, name)); err != nil {
			return false // 本次也未创建
		}
	}
	return true
}

// isNewlyCreated 报告 path 是否为本次执行新建（执行前不存在且当前存在）。
func isNewlyCreated(path string, before map[string]bool, name string) bool {
	if before[name] {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// exitCode 从命令执行错误解析退出码：nil 为 0，ExitError 取其码，其余为 -1。
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// firstWord 返回命令去除前导空白后的首个空白分隔词。
func firstWord(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
