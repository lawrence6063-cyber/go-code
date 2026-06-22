// Package worktree 为并行 Agent 提供基于 git worktree 的物理隔离工作区（LOOP_SPEC §4.5）。
// 每个 Workspace 是一个独立目录 + 临时分支：maker 在其中隔离写，reviewer 审同一 worktree，
// 审查通过则 Merge 并回基线、否则 Discard 清理。相比「写了再回滚」的 diff 暂存（L2），
// worktree 物理隔离更干净，且天然支持多 maker 并行（互不覆盖）。
//
// 本包是依赖图的叶子：仅依赖 sandbox 与标准库，所有 git 操作经注入的 sandbox.Sandbox 执行
// （白名单命令 + 危险命令拦截 + 工作目录约束），模型工具不得直接执行 git worktree（LOOP_SPEC §5.3）。
package worktree

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alaindong/cogent/internal/sandbox"
)

// ErrMergeConflict 表示合并时发生冲突，应由上层映射为 progress 看板的 Blocked 状态交人介入。
var ErrMergeConflict = errors.New("merge conflict")

// ErrDirtyWorktree 表示主仓库工作树存在未提交或未跟踪改动，不满足 worktree 隔离循环的前置条件。
var ErrDirtyWorktree = errors.New("dirty worktree")

// branchPrefix 是 cogent 创建的临时 worktree 分支前缀，便于识别与清理。
const branchPrefix = "cogent/wt-"

// dirPrefix 是 worktree 目录名前缀。
const dirPrefix = "cogent-wt-"

// Workspace 是一个隔离的 git worktree 工作区，承载单个 Agent 的写操作。
type Workspace struct {
	Root   string // worktree 根目录，作为该 Agent 的 sandbox.WorkRoot
	Branch string // 该 worktree 绑定的临时分支
}

// Manager 管理 worktree 的创建、合并与清理。
type Manager interface {
	// Create 从 baseRef 派生一个新 worktree（独立目录 + 临时分支），供 maker 隔离写。
	Create(ctx context.Context, baseRef string) (Workspace, error)
	// Merge 在审查通过后把 worktree 的改动提交并合并回当前基线分支；
	// 冲突返回包装 ErrMergeConflict 的错误交上层处理（不强行推进）。
	Merge(ctx context.Context, ws Workspace, baseRef string) error
	// Discard 丢弃一个 worktree（审查未通过或取消），清理目录与临时分支。
	Discard(ctx context.Context, ws Workspace) error
}

// gitManager 是 Manager 的默认实现：经注入的 sandbox 跑白名单 git 命令。
// sandbox 的 WorkRoot 即主仓库根目录（worktree add / merge / branch 在其内执行）。
type gitManager struct {
	sb      sandbox.Sandbox
	baseDir string // worktree 目录的存放根（默认系统临时区，避免污染主仓库工作区）
}

// New 构造一个基于 git 的 worktree 管理器；worktree 目录默认创建在系统临时区。
// sb 应绑定主仓库根目录（与 cmd 层 gitDiscarder 同构：Enabled=false 继承宿主 PATH 跑 git）。
func New(sb sandbox.Sandbox) Manager {
	return &gitManager{sb: sb, baseDir: os.TempDir()}
}

// EnsureClean 校验主仓库工作树是否干净（无未提交改动与未跟踪文件），作为 worktree 隔离循环的前置条件。
// 自治循环约定「主仓库已处于 baseRef 的干净检出」（见包注释与 Merge 说明）：工作树脏会使
// git merge --no-ff 因 "local/untracked changes would be overwritten" 失败或落盘不干净。
// sb 应绑定主仓库根目录（与 New 注入同构：Enabled=false 继承宿主 PATH 跑 git）。
func EnsureClean(ctx context.Context, sb sandbox.Sandbox) error {
	res, err := sb.Exec(ctx, "git status --porcelain")
	if err != nil {
		return fmt.Errorf("check worktree status: %w", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("check worktree status: git exit %d: %s", res.ExitCode, oneLine(res.Stderr))
	}
	if strings.TrimSpace(res.Stdout) != "" {
		return fmt.Errorf("%w: %d uncommitted/untracked path(s)", ErrDirtyWorktree, countStatusLines(res.Stdout))
	}
	return nil
}

// countStatusLines 统计 git status --porcelain 输出中的非空行数（待处理路径数）。
func countStatusLines(out string) int {
	var n int
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// NewWithBaseDir 构造管理器并指定 worktree 目录存放根（便于测试隔离到 t.TempDir()）。
func NewWithBaseDir(sb sandbox.Sandbox, baseDir string) Manager {
	if strings.TrimSpace(baseDir) == "" {
		baseDir = os.TempDir()
	}
	return &gitManager{sb: sb, baseDir: baseDir}
}

// Create 见 Manager 接口说明：git worktree add -b <branch> <dir> <baseRef>。
func (m *gitManager) Create(ctx context.Context, baseRef string) (Workspace, error) {
	id, err := randID()
	if err != nil {
		return Workspace{}, fmt.Errorf("generate worktree id: %w", err)
	}
	branch := branchPrefix + id
	dir := filepath.Join(m.baseDir, dirPrefix+id)
	cmd := fmt.Sprintf("git worktree add -b %s %s %s",
		shellQuote(branch), shellQuote(dir), shellQuote(baseRef))
	if err := m.run(ctx, cmd); err != nil {
		return Workspace{}, fmt.Errorf("create worktree: %w", err)
	}
	return Workspace{Root: dir, Branch: branch}, nil
}

// Merge 见 Manager 接口说明：先在 worktree 内提交改动，再把其分支合并回主仓库当前基线。
// 约定主仓库当前已在 baseRef 上（自治循环在专用检出上运行）；冲突时 abort 并上抛 ErrMergeConflict。
func (m *gitManager) Merge(ctx context.Context, ws Workspace, baseRef string) error {
	if ws.Root == "" || ws.Branch == "" {
		return errors.New("merge: empty workspace")
	}
	msg := "cogent: merge " + ws.Branch
	commit := fmt.Sprintf("git -C %s add -A && git -C %s commit -m %s --allow-empty",
		shellQuote(ws.Root), shellQuote(ws.Root), shellQuote(msg))
	if err := m.run(ctx, commit); err != nil {
		return fmt.Errorf("commit worktree changes: %w", err)
	}
	merge := fmt.Sprintf("git merge --no-ff --no-edit %s", shellQuote(ws.Branch))
	res, err := m.sb.Exec(ctx, merge)
	if err != nil {
		return fmt.Errorf("merge %s: %w", ws.Branch, err)
	}
	if res.ExitCode != 0 {
		_, _ = m.sb.Exec(ctx, "git merge --abort")
		return fmt.Errorf("%w: %s", ErrMergeConflict, oneLine(res.Stderr))
	}
	return nil
}

// Discard 见 Manager 接口说明：移除 worktree 目录并删除其临时分支（尽力而为，逐步清理）。
func (m *gitManager) Discard(ctx context.Context, ws Workspace) error {
	var errs []error
	if ws.Root != "" {
		remove := fmt.Sprintf("git worktree remove --force %s", shellQuote(ws.Root))
		if res, err := m.sb.Exec(ctx, remove); err != nil {
			errs = append(errs, fmt.Errorf("worktree remove: %w", err))
		} else if res.ExitCode != 0 {
			// worktree 元数据可能已不一致，回落到直接删目录 + prune。
			_ = os.RemoveAll(ws.Root)
			_, _ = m.sb.Exec(ctx, "git worktree prune")
		}
	}
	if ws.Branch != "" {
		del := fmt.Sprintf("git branch -D %s", shellQuote(ws.Branch))
		if res, err := m.sb.Exec(ctx, del); err != nil {
			errs = append(errs, fmt.Errorf("delete branch: %w", err))
		} else if res.ExitCode != 0 {
			errs = append(errs, fmt.Errorf("delete branch %s: %s", ws.Branch, oneLine(res.Stderr)))
		}
	}
	return errors.Join(errs...)
}

// run 执行一条 git 命令并把非零退出码规范化为错误（dangerous 命令由 sandbox 直接拦截上抛）。
func (m *gitManager) run(ctx context.Context, command string) error {
	res, err := m.sb.Exec(ctx, command)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("exit %d: %s", res.ExitCode, oneLine(res.Stderr))
	}
	return nil
}

// randID 生成一个 8 字节十六进制随机标识，保证并行创建的 worktree 目录/分支不冲突。
func randID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// shellQuote 用单引号包裹参数并转义内嵌单引号，避免路径/分支名中的空格或特殊字符破坏命令。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// oneLine 把多行输出压成单行并裁剪首尾空白，便于错误信息呈现（不泄露大段内容）。
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}
