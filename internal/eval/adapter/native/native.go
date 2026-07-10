// Package native 是 EVAL_SPEC §5.4 的 native.Adapter：直接扫描 eval/tasks/，把每个自包含任务
// 目录（task.yaml + task.txt + verify.sh + repo/）读成一条 adapter.Case，不走 Docker。
// 它是 Headless 运行器能跑起来的前置组件（walking skeleton 第一块砖，EVAL_SPEC §8.4）。
//
// 关键不变量（EVAL_SPEC §5.4）：agent 会就地改工作根，故批量跑分必须在任务目录的临时副本上跑，
// 绝不污染 eval/tasks/<name>/ 源（否则第二次跑初始态已被改坏，可解性双校验失效）。
// 副本刻意排除 oracle/ 子目录——参考解绝不喂给 agent（EVAL_SPEC §5.2.1）。
package native

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alaindong/cogent/internal/eval/adapter"
	"github.com/alaindong/cogent/internal/loop"
	"github.com/alaindong/cogent/internal/sandbox"
	"github.com/alaindong/cogent/internal/verify"
)

// defaultVerifyTimeout 是验收脚本单次执行的超时上限（对齐 cmd 的 verifyTimeout）。
const defaultVerifyTimeout = 5 * time.Minute

// Filter 按标签筛选任务；每类为空表示不限，跨类取交集（AND），类内取并集（OR）。
type Filter struct {
	IDs          []string // 只跑这些 id（不含 "native/" 前缀）
	Difficulties []string // easy | medium | hard
	Languages    []string // go | python | ...
	Capabilities []string // convergence | budget | ...
}

// TaskSpec 是一个任务解析后的元数据 + 源路径（尚未建工作区副本），供 dry-run 轻量列出。
type TaskSpec struct {
	Dir     string   // 任务目录绝对路径
	YAML    TaskYAML // task.yaml 解析结果
	Intent  string   // task.txt 全文（喂给 agent 的输入）
	RepoDir string   // <task>/repo 绝对路径（repo-root 任务为空）
}

// Load 扫描 tasksDir 下所有任务目录，解析 task.yaml + 读 task.txt，按 filter 过滤，
// 返回轻量 TaskSpec 列表（不建工作区副本、不改动文件系统）。无 task.yaml 的目录跳过。
func Load(tasksDir string, f Filter) ([]TaskSpec, error) {
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return nil, fmt.Errorf("read tasks dir: %w", err)
	}
	var specs []TaskSpec
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(tasksDir, e.Name())
		spec, ok, err := loadOne(dir)
		if err != nil {
			return nil, err
		}
		if ok && matches(spec.YAML, f) {
			specs = append(specs, spec)
		}
	}
	return specs, nil
}

// loadOne 读取单个任务目录；无 task.yaml 时 ok=false（跳过而非报错）。
func loadOne(dir string) (TaskSpec, bool, error) {
	yamlPath := filepath.Join(dir, "task.yaml")
	data, err := os.ReadFile(yamlPath)
	if os.IsNotExist(err) {
		return TaskSpec{}, false, nil
	}
	if err != nil {
		return TaskSpec{}, false, fmt.Errorf("read %s: %w", yamlPath, err)
	}
	y := parseTaskYAML(data)
	intent, err := os.ReadFile(filepath.Join(dir, "task.txt"))
	if err != nil {
		return TaskSpec{}, false, fmt.Errorf("read task.txt in %s: %w", dir, err)
	}
	spec := TaskSpec{Dir: dir, YAML: y, Intent: strings.TrimSpace(string(intent))}
	if repo := filepath.Join(dir, "repo"); dirExists(repo) {
		spec.RepoDir = repo
	}
	return spec, true, nil
}

// Adapter 把 eval/tasks/ 下的 native 任务映射为 adapter.Case。首版仅支持 workdir==repo
// 的自包含任务；repo-root（自宿主）任务需在干净 clone / worktree 上跑，列入后续里程碑。
type Adapter struct {
	TasksDir      string        // eval/tasks 目录
	WorkspaceDir  string        // 每个 case 工作区副本的根（如 <artifact>/<id>/workspace 的父目录）
	Filter        Filter        // 标签筛选
	VerifyTimeout time.Duration // 验收脚本超时；<=0 用默认
}

// Name 见 adapter.Adapter 接口说明。
func (a Adapter) Name() string { return "native" }

// Prepare 见 adapter.Adapter 接口说明：校验 tasksDir 存在（fail-fast）。
func (a Adapter) Prepare(_ context.Context) error {
	if !dirExists(a.TasksDir) {
		return fmt.Errorf("tasks dir not found: %s", a.TasksDir)
	}
	return nil
}

// Cases 见 adapter.Adapter 接口说明：加载任务、为每个自包含任务建工作区副本并组 Case。
// repo-root 任务当前跳过（首版不支持就地改被测本体）。ctx 取消即停止产出。
func (a Adapter) Cases(ctx context.Context) ([]adapter.Case, error) {
	specs, err := Load(a.TasksDir, a.Filter)
	if err != nil {
		return nil, err
	}
	cases := make([]adapter.Case, 0, len(specs))
	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if spec.YAML.Workdir != "repo" || spec.RepoDir == "" {
			continue // 首版仅支持自包含 repo 任务
		}
		c, err := a.buildCase(spec)
		if err != nil {
			return nil, err
		}
		cases = append(cases, c)
	}
	return cases, nil
}

// buildCase 为一个自包含任务建工作区副本（排除 oracle/）并组装 adapter.Case。
func (a Adapter) buildCase(spec TaskSpec) (adapter.Case, error) {
	id := spec.YAML.ID
	if id == "" {
		id = filepath.Base(spec.Dir)
	}
	workRoot := filepath.Join(a.WorkspaceDir, id, "workspace")
	if err := copyTree(spec.Dir, workRoot, map[string]bool{"oracle": true}); err != nil {
		return adapter.Case{}, fmt.Errorf("copy workspace for %s: %w", id, err)
	}
	return adapter.Case{
		ID: "native/" + id,
		Goal: loop.Goal{
			Intent:   spec.Intent,
			WorkRoot: workRoot,
			Verifier: a.verifier(filepath.Join(spec.Dir, "verify.sh"), filepath.Join(workRoot, "verify.sh")),
			Budget:   budgetFrom(spec.YAML.Budget),
		},
		Meta: adapter.Meta{
			Difficulty:   spec.YAML.Difficulty,
			Languages:    spec.YAML.Languages,
			Capabilities: spec.YAML.Capabilities,
			Source:       "native",
		},
		ExpectedOutcome: expectedOutcome(spec.YAML.ExpectedOutcome),
		Timeout:         parseDuration(spec.YAML.Timeout),
	}, nil
}

// verifier 构造脚本判定器：验收脚本是可信控制面，需继承宿主 PATH（go 工具链），故沙箱
// Enabled=false（仍保留危险命令拦截 + WorkRoot 约束 + 超时）。NewSandbox 按 workRoot 构造，
// 使脚本跑在改动所在的副本目录（同 cmd 的 buildVerifier）。用 pristineVerifier 包一层：
// 每次判定前用任务源目录的 pristine verify.sh 覆盖工作区副本，抹掉 agent 对判定脚本的篡改
// （verifier independence，EVAL_SPEC §4.3/§7）。
func (a Adapter) verifier(pristineScript, wsScript string) verify.Verifier {
	timeout := a.VerifyTimeout
	if timeout <= 0 {
		timeout = defaultVerifyTimeout
	}
	inner := verify.ScriptVerifier{
		Script: wsScript,
		NewSandbox: func(root string) sandbox.Sandbox {
			if strings.TrimSpace(root) == "" {
				root = filepath.Dir(wsScript)
			}
			return sandbox.New(sandbox.Config{WorkRoot: root, Enabled: false, Timeout: timeout})
		},
	}
	return pristineVerifier{pristine: pristineScript, target: wsScript, inner: inner}
}

// pristineVerifier 在每次判定前用任务源目录的 pristine verify.sh 覆盖工作区副本里的同名脚本，
// 抹掉 agent 可能对判定脚本的篡改（verifier independence，EVAL_SPEC §4.3/§7）：agent 只能改
// 工作区的 repo（判据据此看到真实改动），却改不动被判定实际执行的 pristine 脚本。
type pristineVerifier struct {
	pristine string                // 任务源目录的 verify.sh（agent sandbox 够不到）
	target   string                // 工作区副本里的 verify.sh（每次判定前被 pristine 覆盖）
	inner    verify.ScriptVerifier // 底层脚本判定器（跑 target，脚本内部 cd 到工作区 repo）
}

// Verify 见 verify.Verifier 接口说明：先恢复 pristine 判定脚本，再执行底层脚本判定。
// 恢复失败按 fail-closed 处理（判定视为未通过）。
func (v pristineVerifier) Verify(ctx context.Context, workRoot, goalIntent string) (verify.Report, error) {
	if err := copyFile(v.pristine, v.target); err != nil {
		return verify.Report{Summary: "restore pristine verify.sh failed: " + err.Error()},
			fmt.Errorf("restore verify: %w", err)
	}
	return v.inner.Verify(ctx, workRoot, goalIntent)
}

// budgetFrom 把 BudgetYAML 映射为 loop.Budget：以 DefaultBudget 兜底，仅覆盖显式给定字段。
func budgetFrom(b BudgetYAML) loop.Budget {
	out := loop.DefaultBudget()
	if b.MaxIterations > 0 {
		out.MaxIterations = b.MaxIterations
	}
	if b.MaxCostUSD > 0 {
		out.MaxCostUSD = b.MaxCostUSD
	}
	if d := parseDuration(b.MaxWallClock); d > 0 {
		out.MaxWallClock = d
	}
	return out
}

// expectedOutcome 归一期望结局；空值按 achieved 兜底。
func expectedOutcome(s string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return "achieved"
}

// parseDuration 解析形如 "5m" 的时长；非法或空返回 0。
func parseDuration(s string) time.Duration {
	if s = strings.TrimSpace(s); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			return d
		}
	}
	return 0
}

// matches 判断任务是否满足筛选条件（每类 OR、跨类 AND；空类不限）。
func matches(y TaskYAML, f Filter) bool {
	return anyIn(f.IDs, []string{y.ID}) &&
		anyIn(f.Difficulties, []string{y.Difficulty}) &&
		anyIn(f.Languages, y.Languages) &&
		anyIn(f.Capabilities, y.Capabilities)
}

// anyIn 报告 want 为空（不限）或 have 中任一元素命中 want。
func anyIn(want, have []string) bool {
	if len(want) == 0 {
		return true
	}
	for _, w := range want {
		for _, h := range have {
			if strings.EqualFold(w, h) {
				return true
			}
		}
	}
	return false
}

// dirExists 报告 path 是否为已存在的目录。
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// copyTree 递归复制 src 到 dst，跳过 src 顶层名字命中 skipTop 的子目录（如 oracle）。
// 目标已存在则先清空，保证副本干净。
func copyTree(src, dst string, skipTop map[string]bool) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if skipTop[e.Name()] {
			continue
		}
		if err := copyEntry(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()), e); err != nil {
			return err
		}
	}
	return nil
}

// copyEntry 复制单个目录项（目录递归、普通文件按内容+权限复制，符号链接等忽略）。
func copyEntry(src, dst string, e os.DirEntry) error {
	if e.IsDir() {
		return copyTree(src, dst, nil)
	}
	if !e.Type().IsRegular() {
		return nil
	}
	return copyFile(src, dst)
}

// copyFile 按内容与权限复制单个普通文件。
func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
