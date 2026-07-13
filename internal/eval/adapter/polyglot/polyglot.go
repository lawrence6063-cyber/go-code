// Package polyglot 是 EVAL_SPEC §5.2 的 polyglot.Adapter（接入模式 B，无 Docker）：把 aider
// polyglot-benchmark 数据集（六语言 Exercism 练习）映射为 cogent 的 adapter.Case。它是继 native
// 之后第二个 Adapter，用来验证 Adapter 抽象可泛化到外部基准，并一步点亮「多语言 + 反馈收敛」维度
// （EVAL_SPEC §5.2.3 个人项目最小可行路径第 1 步）。
//
// 数据集不随仓库分发（守零依赖），由用户自行 clone 到 Root 指定目录；Adapter 只读取、不联网拉取。
//
// 关键不变量（对齐 native.Adapter）：
//   - 工作区副本隔离——每个练习复制到临时副本再跑，绝不污染数据集源目录；
//   - 参考解不泄露——副本刻意排除 .meta/（含 example 参考实现，EVAL_SPEC §5.2.1「gold 绝不喂 agent」）；
//   - 测试文件 pristine——判定前用源目录的测试文件覆盖工作区副本，抹掉 agent 对测试的篡改（§4.3/§7）。
package polyglot

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/alaindong/cogent/internal/eval/adapter"
	"github.com/alaindong/cogent/internal/loop"
	"github.com/alaindong/cogent/internal/sandbox"
	"github.com/alaindong/cogent/internal/verify"
)

// defaultVerifyTimeout 是测试脚本单次执行的超时上限（多语言构建/测试偏慢，给足余量）。
const defaultVerifyTimeout = 10 * time.Minute

// practiceRel 是各语言练习目录相对 <root>/<lang> 的固定子路径（Exercism 布局）。
const practiceRel = "exercises/practice"

// Filter 按语言 / 练习 slug 筛选，并可限制每语言取样数量（个人项目取子集，EVAL_SPEC §5.2.3）。
type Filter struct {
	Languages []string // 只跑这些语言（空=全部受支持语言）
	Exercises []string // 只跑这些练习 slug（空=不限）
	Limit     int      // 每语言最多取 N 个练习（<=0=不限）
}

// ExerciseSpec 是一个练习解析后的元数据 + 源路径（尚未建工作区副本），供 dry-run 轻量列出。
type ExerciseSpec struct {
	Language string     // 语言标识
	Slug     string     // 练习 slug（目录名）
	Dir      string     // 练习目录绝对路径
	Config   metaConfig // .meta/config.json 解析结果
}

// Adapter 把 polyglot-benchmark 数据集映射为 adapter.Case（实现 adapter.Adapter）。
type Adapter struct {
	Root          string        // 数据集根目录（用户 clone 的 polyglot-benchmark）
	WorkspaceDir  string        // 每个 case 工作区副本的根
	Filter        Filter        // 语言 / 练习 / 数量筛选
	VerifyTimeout time.Duration // 测试脚本超时；<=0 用默认
}

// Name 见 adapter.Adapter 接口说明。
func (a Adapter) Name() string { return "polyglot" }

// Prepare 见 adapter.Adapter 接口说明：校验数据集根存在且至少含一门受支持语言的练习目录（fail-fast）。
func (a Adapter) Prepare(_ context.Context) error {
	if !dirExists(a.Root) {
		return fmt.Errorf("polyglot dataset root not found: %s (clone Aider-AI/polyglot-benchmark first)", a.Root)
	}
	for name := range supportedLangs {
		if dirExists(filepath.Join(a.Root, name, practiceRel)) {
			return nil
		}
	}
	return fmt.Errorf("no supported language track found under %s (expected e.g. go/%s)", a.Root, practiceRel)
}

// Load 扫描数据集，按 filter 解析出练习列表（不建工作区副本、不改文件系统），供 dry-run 与 Cases 复用。
// 结果按 语言→slug 字典序稳定排序。
func Load(root string, f Filter) ([]ExerciseSpec, error) {
	var specs []ExerciseSpec
	for _, lang := range selectedLangs(f.Languages) {
		practice := filepath.Join(root, lang, practiceRel)
		if !dirExists(practice) {
			continue
		}
		langSpecs, err := loadLang(lang, practice, f)
		if err != nil {
			return nil, err
		}
		specs = append(specs, langSpecs...)
	}
	sort.Slice(specs, func(i, j int) bool {
		if specs[i].Language != specs[j].Language {
			return specs[i].Language < specs[j].Language
		}
		return specs[i].Slug < specs[j].Slug
	})
	return specs, nil
}

// loadLang 加载单门语言 practice 目录下、命中筛选的练习（应用 per-language Limit）。
func loadLang(lang, practice string, f Filter) ([]ExerciseSpec, error) {
	entries, err := os.ReadDir(practice)
	if err != nil {
		return nil, fmt.Errorf("read practice dir %s: %w", practice, err)
	}
	var out []ExerciseSpec
	for _, e := range entries {
		if !e.IsDir() || !slugSelected(e.Name(), f.Exercises) {
			continue
		}
		spec, ok, err := loadExercise(lang, filepath.Join(practice, e.Name()))
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		out = append(out, spec)
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out, nil
}

// loadExercise 读取单个练习目录；缺 .meta/config.json 的目录跳过（ok=false）。
func loadExercise(lang, dir string) (ExerciseSpec, bool, error) {
	cfgPath := filepath.Join(dir, ".meta", "config.json")
	if !fileExists(cfgPath) {
		return ExerciseSpec{}, false, nil
	}
	cfg, err := readMetaConfig(cfgPath)
	if err != nil {
		return ExerciseSpec{}, false, fmt.Errorf("exercise %s/%s: %w", lang, filepath.Base(dir), err)
	}
	return ExerciseSpec{Language: lang, Slug: filepath.Base(dir), Dir: dir, Config: cfg}, true, nil
}

// Cases 见 adapter.Adapter 接口说明：加载练习、为每个练习建工作区副本并组 Case。ctx 取消即停止产出。
func (a Adapter) Cases(ctx context.Context) ([]adapter.Case, error) {
	specs, err := Load(a.Root, a.Filter)
	if err != nil {
		return nil, err
	}
	cases := make([]adapter.Case, 0, len(specs))
	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c, err := a.buildCase(spec)
		if err != nil {
			return nil, err
		}
		cases = append(cases, c)
	}
	return cases, nil
}

// buildCase 为一个练习建工作区副本（排除 .meta/ 参考解）、生成判定脚本，并组装 adapter.Case。
// 工作区目录**刻意命名为 slug**（而非 "workspace"）：cpp 的 Exercism CMakeLists 由目录名推导工程名与
// 源文件名，目录名必须等于练习 slug 才能正确构建。
func (a Adapter) buildCase(spec ExerciseSpec) (adapter.Case, error) {
	ls, ok := langOf(spec.Language)
	if !ok {
		return adapter.Case{}, fmt.Errorf("unsupported language: %s", spec.Language)
	}
	caseDir := filepath.Join(a.WorkspaceDir, spec.Language+"_"+spec.Slug)
	workRoot := filepath.Join(caseDir, spec.Slug)
	if err := copyTree(spec.Dir, workRoot, map[string]bool{".meta": true}); err != nil {
		return adapter.Case{}, fmt.Errorf("copy workspace for %s/%s: %w", spec.Language, spec.Slug, err)
	}
	script, err := writeVerifyScript(caseDir, workRoot, ls.testCmd)
	if err != nil {
		return adapter.Case{}, err
	}
	return adapter.Case{
		ID:   "polyglot/" + spec.Language + "/" + spec.Slug,
		Goal: loop.Goal{Intent: a.intent(spec, ls), WorkRoot: workRoot, Verifier: a.verifier(spec, workRoot, script), Budget: loop.DefaultBudget()},
		Meta: adapter.Meta{
			Languages:    []string{spec.Language},
			Capabilities: []string{"convergence"},
			Source:       "polyglot",
		},
		ExpectedOutcome: "achieved",
		Timeout:         a.timeout(),
	}, nil
}

// intent 构造喂给 agent 的自然语言意图：题面 instructions + 明确「改哪些文件、勿动测试、如何验证」。
func (a Adapter) intent(spec ExerciseSpec, ls langSpec) string {
	var b strings.Builder
	b.WriteString(readInstructions(spec.Dir))
	b.WriteString("\n\n---\n")
	fmt.Fprintf(&b, "Language: %s. Implement the solution in the following file(s):\n", spec.Language)
	for _, f := range spec.Config.Files.Solution {
		fmt.Fprintf(&b, "  - %s\n", f)
	}
	b.WriteString("Do NOT modify any test files. When done, the tests must pass via: ")
	b.WriteString(ls.testCmd)
	b.WriteString("\n")
	return b.String()
}

// timeout 返回单练习墙钟硬上限（VerifyTimeout 未设时用默认）。
func (a Adapter) timeout() time.Duration {
	if a.VerifyTimeout > 0 {
		return a.VerifyTimeout
	}
	return defaultVerifyTimeout
}

// verifier 构造该练习的判定器：ScriptVerifier 跑工作区外的生成脚本（agent 够不到，不可篡改），
// 并用 testVerifier 包一层——判定前把源目录的测试文件覆盖回工作区副本（测试文件 pristine，§4.3/§7）。
func (a Adapter) verifier(spec ExerciseSpec, workRoot, script string) verify.Verifier {
	inner := verify.ScriptVerifier{
		Script: script,
		NewSandbox: func(root string) sandbox.Sandbox {
			if strings.TrimSpace(root) == "" {
				root = workRoot
			}
			return sandbox.New(sandbox.Config{WorkRoot: root, Enabled: false, Timeout: a.timeout()})
		},
	}
	return testVerifier{restore: restorePairs(spec, workRoot), inner: inner}
}

// restorePairs 计算需 pristine 恢复的文件（源→工作区副本）对：测试文件 + editor 只读辅助文件
// （如 go 的 cases_test.go，测试编译依赖），均是 agent 不应改动、判定应以源为准的文件。
func restorePairs(spec ExerciseSpec, workRoot string) []restorePair {
	rels := append(append([]string{}, spec.Config.Files.Test...), spec.Config.Files.Editor...)
	pairs := make([]restorePair, 0, len(rels))
	for _, rel := range rels {
		pairs = append(pairs, restorePair{src: filepath.Join(spec.Dir, rel), dst: filepath.Join(workRoot, rel)})
	}
	return pairs
}

// restorePair 记录一个测试文件的 pristine 源路径与工作区目标路径。
type restorePair struct {
	src string // 数据集源目录里的测试文件（agent sandbox 够不到）
	dst string // 工作区副本里的同名测试文件（每次判定前被 src 覆盖）
}

// testVerifier 在每次判定前用 pristine 测试文件覆盖工作区副本，抹掉 agent 对测试的篡改
// （verifier independence，EVAL_SPEC §4.3/§7）：agent 只能改解题文件（判据据此看真实改动），
// 却改不动被判定实际执行的测试文件。inner 为接口便于单测注入替身（免真实工具链）。
type testVerifier struct {
	restore []restorePair   // 待恢复的测试文件对
	inner   verify.Verifier // 底层判定器（脚本跑语言测试命令）
}

// Verify 见 verify.Verifier 接口说明：先恢复 pristine 测试文件，再执行底层判定。
// 恢复失败按 fail-closed 处理（判定视为未通过）。
func (v testVerifier) Verify(ctx context.Context, workRoot, goalIntent string) (verify.Report, error) {
	for _, p := range v.restore {
		if err := copyFile(p.src, p.dst); err != nil {
			return verify.Report{Summary: "restore pristine test failed: " + err.Error()},
				fmt.Errorf("restore test %s: %w", filepath.Base(p.dst), err)
		}
	}
	return v.inner.Verify(ctx, workRoot, goalIntent)
}

// writeVerifyScript 把语言测试命令生成为工作区外的判定脚本（cd 进工作区根后执行，退出码即判据）。
// 脚本置于 caseDir（工作区的父目录），agent sandbox 限于 workRoot，够不到、无法篡改判定脚本。
func writeVerifyScript(caseDir, workRoot, testCmd string) (string, error) {
	absWork, err := filepath.Abs(workRoot)
	if err != nil {
		return "", fmt.Errorf("abs workRoot: %w", err)
	}
	script := filepath.Join(caseDir, "verify.sh")
	content := fmt.Sprintf("#!/usr/bin/env bash\nset -euo pipefail\ncd %q\n%s\n", absWork, testCmd)
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		return "", fmt.Errorf("write verify script: %w", err)
	}
	return script, nil
}

// readInstructions 读取练习的题面（.docs/instructions.md，可叠加 introduction.md）；缺失返回占位提示。
func readInstructions(dir string) string {
	var b strings.Builder
	for _, name := range []string{"introduction.md", "instructions.md"} {
		if data, err := os.ReadFile(filepath.Join(dir, ".docs", name)); err == nil {
			b.Write(data)
			b.WriteString("\n")
		}
	}
	if b.Len() == 0 {
		return "(no instructions provided; implement the solution so the tests pass)"
	}
	return strings.TrimSpace(b.String())
}

// selectedLangs 返回要扫描的语言列表：want 为空则取全部受支持语言（字典序），否则取 want 与受支持集的交集。
func selectedLangs(want []string) []string {
	if len(want) == 0 {
		out := make([]string, 0, len(supportedLangs))
		for name := range supportedLangs {
			out = append(out, name)
		}
		sort.Strings(out)
		return out
	}
	out := make([]string, 0, len(want))
	for _, w := range want {
		if _, ok := supportedLangs[strings.ToLower(w)]; ok {
			out = append(out, strings.ToLower(w))
		}
	}
	return out
}

// slugSelected 报告 slug 是否命中筛选（want 为空=不限）。
func slugSelected(slug string, want []string) bool {
	if len(want) == 0 {
		return true
	}
	for _, w := range want {
		if strings.EqualFold(w, slug) {
			return true
		}
	}
	return false
}

// dirExists 报告 path 是否为已存在的目录。
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// fileExists 报告 path 是否为已存在的普通文件。
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// copyTree 递归复制 src 到 dst，跳过 src 顶层名字命中 skipTop 的子目录（如 .meta）。目标已存在则先清空。
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

// copyFile 按内容与权限复制单个普通文件，必要时创建父目录。
func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
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
