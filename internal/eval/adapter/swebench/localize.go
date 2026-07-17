// 本文件实现 SCAFFOLD_SPEC §4.4（S-E）结构化定位在 swebench Adapter 侧的接线：
// 在 repair 提示前，用 scaffold 的轻量 BM25 对工作区源文件按 issue 关键词排序，取 top-k 注入定位线索，
// 收窄 agent 的探索范围。默认关闭（env 门控），开启不改判定语义、只丰富提示。
//
// 合规：只读工作区（已 clone 的隔离副本）源码，绝不读取隐藏 test_patch / FAIL_TO_PASS（守 §6 红线）。
package swebench

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/alaindong/cogent/internal/eval/scaffold"
)

// 定位相关环境变量与默认。
const (
	localizeEnvVar     = "COGENT_SWEBENCH_LOCALIZE"   // 是否启用结构化定位（默认关闭）
	localizeKEnvVar    = "COGENT_SWEBENCH_LOCALIZE_K" // 注入的 top-k 文件数（默认 10）
	defaultLocalizeK   = 10
	localizeMaxFiles   = 4000      // 扫描文件数上限（防超大仓库爆内存/耗时）
	localizeReadBytes  = 8 << 10   // 每文件读取的内容前缀字节数（片段足够 BM25 命中）
	localizeMaxFileLen = 512 << 10 // 超过此大小的文件跳过（多为生成/数据文件）
)

// localizeEnabled 报告是否启用结构化定位（仅显式开启值才启用，默认关闭以不扰动基线 A/B）。
func localizeEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(localizeEnvVar))) {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}

// localizeK 返回注入的 top-k 文件数（env 覆盖，非法/缺省用默认）。
func localizeK() int {
	if v := strings.TrimSpace(os.Getenv(localizeKEnvVar)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultLocalizeK
}

// localizeHint 扫描 workRoot 源文件、按 inst 的 issue 文本 BM25 排序，返回 top-k 定位提示段。
// 未启用或无命中时返回空串（调用方按需拼接，不影响未开启时的行为）。
func localizeHint(workRoot string, inst Instance) string {
	if !localizeEnabled() {
		return ""
	}
	docs := collectDocs(workRoot)
	if len(docs) == 0 {
		return ""
	}
	top := scaffold.RankFiles(inst.ProblemStatement, docs, localizeK())
	if len(top) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nLikely relevant files (ranked by issue relevance; a heuristic hint, NOT exhaustive — " +
		"verify by reading before editing, and look elsewhere if the root cause is not here):\n")
	for _, p := range top {
		b.WriteString("- ")
		b.WriteString(p)
		b.WriteString("\n")
	}
	return b.String()
}

// collectDocs 有界遍历 workRoot，收集源码文件为 scaffold.Doc（路径 + 内容前缀），跳过测试/vendor/构建产物。
func collectDocs(workRoot string) []scaffold.Doc {
	var docs []scaffold.Doc
	_ = filepath.WalkDir(workRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 单文件错误不中断整体遍历
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if len(docs) >= localizeMaxFiles {
			return filepath.SkipAll
		}
		if !isSourceFile(path) {
			return nil
		}
		rel, relErr := filepath.Rel(workRoot, path)
		if relErr != nil {
			return nil
		}
		docs = append(docs, scaffold.Doc{Path: filepath.ToSlash(rel), Text: readPrefix(path)})
		return nil
	})
	return docs
}

// skipDir 报告目录名是否应跳过（VCS/依赖/测试/构建/文档），避免噪声与体积。
func skipDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "node_modules", "vendor", "_vendor", "site-packages",
		"dist", "build", "__pycache__", ".tox", ".venv", "docs", "test", "tests", "testing":
		return true
	}
	return false
}

// isSourceFile 报告路径是否为参与定位的源码文件（按扩展名白名单），并排除明显的测试文件名。
func isSourceFile(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") ||
		strings.HasSuffix(base, "_test.go") {
		return false
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".py", ".go", ".js", ".ts", ".jsx", ".tsx", ".java", ".rb", ".rs",
		".c", ".cc", ".cpp", ".h", ".hpp", ".php", ".scala", ".kt", ".cs":
		return true
	}
	return false
}

// readPrefix 读取文件前 localizeReadBytes 字节作为打分文本；超大文件跳过内容（仅留路径 token）。
func readPrefix(path string) string {
	info, err := os.Stat(path)
	if err != nil || info.Size() > localizeMaxFileLen {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, localizeReadBytes)
	n, _ := f.Read(buf)
	return string(buf[:n])
}
