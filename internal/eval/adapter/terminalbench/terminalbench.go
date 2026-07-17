// Package terminalbench 是 EVAL_SPEC §5.2 的 terminalbench.Adapter：把 Terminal-Bench 数据集的
// 「指令 + 测试脚本 + oracle 参考解」任务映射为 cogent 的 adapter.Case，是继 native / polyglot /
// swebench 之后第四个 Adapter，用于覆盖「运行期 / 跨领域困难任务」维度（EVAL_SPEC §4.6、§5.2.3 第 3 步）。
//
// Terminal-Bench 任务本质是 Docker 环境（每任务 Dockerfile + run-tests.sh + tests/ + solution.sh）。
// 本 Adapter 走**接入模式 B**（EVAL_SPEC §5.2.1）：把任务的 run-tests.sh 测试脚本包成判定器，在隔离
// 工作区判定。无 Docker 环境下仅适用于「纯文件系统 / 脚本」类任务；依赖容器内包/服务的任务需官方 Harbor
// （模式 A，需 Docker）——本仓的 fixture oracle 测试用自包含 Docker-free 任务在无 Docker 下验证判定路径。
//
// 关键不变量（对齐 native / polyglot / swebench）：
//   - 数据集由用户预先 clone（不联网拉取）；工作区隔离——每个任务复制到临时副本再跑；
//   - oracle 参考解（solution.sh）绝不喂 agent；隐藏测试（run-tests.sh + tests/）判定时瞬态注入、判完移除
//     （agent 全程看不到判定测试，也无法篡改被实际执行的测试，EVAL_SPEC §4.3/§7/§5.2.1）。
package terminalbench

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/alaindong/cogent/internal/eval/adapter"
	"github.com/alaindong/cogent/internal/loop"
	"github.com/alaindong/cogent/internal/verify"
)

// defaultVerifyTimeout 是测试脚本单次执行的超时上限（运行期任务偏慢，给足余量）。
const defaultVerifyTimeout = 15 * time.Minute

// runTestsScript 是 Terminal-Bench 任务的标准测试入口脚本名（判定用，隐藏于 agent）。
const runTestsScript = "run-tests.sh"

// hiddenNames 是不复制进 agent 工作区的控制面/判定资产（参考解 + 隐藏测试 + 容器配置 + 元数据）。
var hiddenNames = map[string]bool{
	"task.yaml": true, "solution.sh": true, "solution.yaml": true,
	runTestsScript: true, "tests": true,
	"Dockerfile": true, "docker-compose.yaml": true, "docker-compose.yml": true,
	".git": true,
}

// TaskSpec 是一个任务解析后的元数据 + 源路径（尚未建工作区副本），供 dry-run 轻量列出。
type TaskSpec struct {
	ID   string   // 任务标识（目录名）
	Dir  string   // 任务目录绝对路径
	YAML TaskYAML // task.yaml 解析结果
}

// Adapter 把 Terminal-Bench 数据集映射为 adapter.Case（实现 adapter.Adapter）。
type Adapter struct {
	DatasetDir    string        // 数据集根目录（含 tasks/ 或直接是 tasks 目录）
	WorkspaceDir  string        // 每个 case 工作区副本的根
	Filter        Filter        // task id / tag / 难度 / 数量筛选
	VerifyTimeout time.Duration // 测试脚本超时；<=0 用默认
}

// Name 见 adapter.Adapter 接口说明。
func (a Adapter) Name() string { return "terminalbench" }

// Prepare 见 adapter.Adapter 接口说明：校验数据集目录存在且能定位到 tasks 目录（fail-fast）。
func (a Adapter) Prepare(_ context.Context) error {
	if strings.TrimSpace(a.DatasetDir) == "" || !dirExists(a.DatasetDir) {
		return fmt.Errorf("terminal-bench dataset dir not found: %s (clone the dataset first)", a.DatasetDir)
	}
	if tasksDir(a.DatasetDir) == "" {
		return fmt.Errorf("no tasks found under %s (expected <dir>/tasks/<id>/task.yaml or <dir>/<id>/task.yaml)", a.DatasetDir)
	}
	return nil
}

// Load 扫描数据集下的任务目录，解析 task.yaml，按 filter 过滤，返回轻量 TaskSpec 列表（不建副本）。
// 结果按 id 字典序稳定排序。无 task.yaml 的目录跳过。
func Load(datasetDir string, f Filter) ([]TaskSpec, error) {
	root := tasksDir(datasetDir)
	if root == "" {
		return nil, fmt.Errorf("no tasks dir under %s", datasetDir)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read tasks dir %s: %w", root, err)
	}
	var specs []TaskSpec
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		data, err := os.ReadFile(filepath.Join(dir, "task.yaml"))
		if err != nil {
			continue // 无 task.yaml 的目录跳过
		}
		spec := TaskSpec{ID: e.Name(), Dir: dir, YAML: parseTaskYAML(data)}
		if matches(spec, f) {
			specs = append(specs, spec)
			if f.Limit > 0 && len(specs) >= f.Limit {
				break
			}
		}
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	return specs, nil
}

// Cases 见 adapter.Adapter 接口说明：加载任务、为每个任务建工作区副本并组 Case。ctx 取消即停止产出。
func (a Adapter) Cases(ctx context.Context) ([]adapter.Case, error) {
	specs, err := Load(a.DatasetDir, a.Filter)
	if err != nil {
		return nil, err
	}
	cases := make([]adapter.Case, 0, len(specs))
	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !fileExists(filepath.Join(spec.Dir, runTestsScript)) {
			continue // 无 run-tests.sh 的任务本 Adapter（模式 B）无法判定，跳过
		}
		c, err := a.buildCase(spec)
		if err != nil {
			return nil, err
		}
		cases = append(cases, c)
	}
	return cases, nil
}

// buildCase 为一个任务建工作区副本（排除隐藏判定资产与参考解）并组装 adapter.Case。
func (a Adapter) buildCase(spec TaskSpec) (adapter.Case, error) {
	workRoot := filepath.Join(a.WorkspaceDir, sanitize(spec.ID), "workspace")
	if err := copyTree(spec.Dir, workRoot, hiddenNames); err != nil {
		return adapter.Case{}, fmt.Errorf("copy workspace for %s: %w", spec.ID, err)
	}
	return adapter.Case{
		ID:              "terminalbench/" + spec.ID,
		Goal:            loop.Goal{Intent: a.intent(spec), WorkRoot: workRoot, Verifier: a.verifier(spec), Budget: loop.DefaultBudget()},
		Meta:            adapter.Meta{Difficulty: difficultyOf(spec.YAML), Capabilities: capabilitiesOf(spec.YAML), Source: "terminalbench"},
		ExpectedOutcome: "achieved",
		Timeout:         a.timeout(),
	}, nil
}

// verifier 构造该任务的判定器：判定前把隐藏测试资产（run-tests.sh + tests/）瞬态注入工作区、跑
// run-tests.sh、判完移除（测试对 agent 隐藏且 pristine，EVAL_SPEC §4.3/§7）。
func (a Adapter) verifier(spec TaskSpec) verify.Verifier {
	return taskVerifier{srcDir: spec.Dir, runScript: runTestsScript, timeout: a.timeout()}
}

// intent 构造喂给 agent 的自然语言意图：任务指令 + 明确「在工作目录内完成、有隐藏测试判定、勿造测试」。
// 刻意不透露 run-tests.sh / tests/ / solution（隐藏判定与参考解），避免面向测试作弊。
func (a Adapter) intent(spec TaskSpec) string {
	instr := spec.YAML.Instruction
	if strings.TrimSpace(instr) == "" {
		instr = "(no instruction provided in task.yaml)"
	}
	return instr + "\n\n---\n" +
		"Work within the current directory to accomplish the task. A hidden test suite will judge your work; " +
		"do NOT create or modify any test files or test runner. Make the changes the task asks for.\n"
}

// timeout 返回单任务墙钟硬上限（VerifyTimeout 未设时用默认）。
func (a Adapter) timeout() time.Duration {
	if a.VerifyTimeout > 0 {
		return a.VerifyTimeout
	}
	return defaultVerifyTimeout
}

// difficultyOf 归一难度；Terminal-Bench 缺省按 hard（其定位为困难任务集）。
func difficultyOf(y TaskYAML) string {
	if d := strings.TrimSpace(y.Difficulty); d != "" {
		return d
	}
	return "hard"
}

// capabilitiesOf 组合维度标签：固定含 runtime + exploration，叠加任务自身 tags（去重由报告层容忍）。
func capabilitiesOf(y TaskYAML) []string {
	caps := []string{"runtime", "exploration"}
	return append(caps, y.Tags...)
}

// matches 判断任务是否满足筛选（每类 OR、跨类 AND；空类不限）。
func matches(spec TaskSpec, f Filter) bool {
	return anyEqual(f.IDs, spec.ID) &&
		anyEqual(f.Difficulties, difficultyOf(spec.YAML)) &&
		anyIntersect(f.Tags, spec.YAML.Tags)
}

// tasksDir 解析数据集的任务根目录：优先 <dir>/tasks，其次 <dir> 自身（当其直接含任务子目录时）。
func tasksDir(datasetDir string) string {
	if d := filepath.Join(datasetDir, "tasks"); dirExists(d) {
		return d
	}
	if hasTaskSubdir(datasetDir) {
		return datasetDir
	}
	return ""
}

// hasTaskSubdir 报告 dir 下是否存在任一含 task.yaml 的子目录。
func hasTaskSubdir(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() && fileExists(filepath.Join(dir, e.Name(), "task.yaml")) {
			return true
		}
	}
	return false
}
