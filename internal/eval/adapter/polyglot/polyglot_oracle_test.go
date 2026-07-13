package polyglot

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"
)

// oracleToolBin 返回验证某语言参考解所需的关键工具二进制（缺失则跳过该语言）。
func oracleToolBin(lang string) string {
	switch lang {
	case "go":
		return "go"
	case "python":
		return "python3"
	case "rust":
		return "cargo"
	case "javascript":
		return "npm"
	case "java":
		return "java"
	case "cpp":
		return "cmake"
	}
	return ""
}

// TestOracleSolutionsPass 是受 POLYGLOT_DIR 门控的集成测试（EVAL_SPEC §5.1 可解性自证的 polyglot 版）：
// 对每门语言取若干练习，用数据集自带的参考解（example）代替 agent 产出、走**真实 adapter + verifier
// 代码路径**跑通测试，断言判定通过。这在不花 LLM 钱的前提下证明：数据集加载、工作区隔离、六语言测试命令、
// 测试文件 pristine 恢复全部正确。缺工具链的语言自动跳过；每语言练习数由 POLYGLOT_ORACLE_LIMIT 控制（默认 1）。
//
// 运行示例：
//
//	POLYGLOT_DIR=~/.cache/cogent-eval/polyglot-benchmark POLYGLOT_ORACLE_LIMIT=1 \
//	  go test ./internal/eval/adapter/polyglot -run TestOracleSolutionsPass -v -timeout 30m
func TestOracleSolutionsPass(t *testing.T) {
	root := os.Getenv("POLYGLOT_DIR")
	if root == "" {
		t.Skip("POLYGLOT_DIR not set; skipping polyglot oracle integration test")
	}
	limit := 1
	if v := os.Getenv("POLYGLOT_ORACLE_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	for _, lang := range sortedSupportedLangs() {
		t.Run(lang, func(t *testing.T) {
			if bin := oracleToolBin(lang); bin != "" {
				if _, err := exec.LookPath(bin); err != nil {
					t.Skipf("toolchain %q not on PATH; skipping %s", bin, lang)
				}
			}
			runOracleLang(t, root, lang, limit)
		})
	}
}

// runOracleLang 加载某语言前 limit 个练习，逐个套用参考解并断言判定通过。
func runOracleLang(t *testing.T, root, lang string, limit int) {
	t.Helper()
	specs, err := Load(root, Filter{Languages: []string{lang}, Limit: limit})
	if err != nil {
		t.Fatalf("load %s: %v", lang, err)
	}
	if len(specs) == 0 {
		t.Skipf("no exercises found for %s under %s", lang, root)
	}
	for _, spec := range specs {
		a := Adapter{Root: root, WorkspaceDir: t.TempDir(), VerifyTimeout: 12 * time.Minute}
		c, err := a.buildCase(spec)
		if err != nil {
			t.Fatalf("buildCase %s/%s: %v", lang, spec.Slug, err)
		}
		if err := applyOracle(spec, c.Goal.WorkRoot); err != nil {
			t.Fatalf("apply oracle %s/%s: %v", lang, spec.Slug, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
		rep, err := c.Goal.Verifier.Verify(ctx, c.Goal.WorkRoot, c.Goal.Intent)
		cancel()
		if err != nil {
			t.Fatalf("verify %s/%s errored: %v\n%s", lang, spec.Slug, err, rep.Detail)
		}
		if !rep.Passed {
			t.Errorf("oracle solution should PASS for %s/%s: %s\n%s", lang, spec.Slug, rep.Summary, tail(rep.Detail, 1200))
		} else {
			t.Logf("oracle PASS %s/%s", lang, spec.Slug)
		}
	}
}

// applyOracle 把参考解 example[i] 复制到 solution[i]（模拟 agent 交出正确答案）。
// example 长度 ≤ solution（如 rust 的 solution 含 Cargo.toml 无对应 example），按位置映射前若干项。
func applyOracle(spec ExerciseSpec, workRoot string) error {
	ex, sol := spec.Config.Files.Example, spec.Config.Files.Solution
	if len(ex) == 0 {
		return fmt.Errorf("no example files declared")
	}
	for i, e := range ex {
		if i >= len(sol) {
			break
		}
		if err := copyFile(filepath.Join(spec.Dir, e), filepath.Join(workRoot, sol[i])); err != nil {
			return err
		}
	}
	return nil
}

// sortedSupportedLangs 返回受支持语言的字典序列表（测试确定性）。
func sortedSupportedLangs() []string {
	out := make([]string, 0, len(supportedLangs))
	for name := range supportedLangs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// tail 返回字符串末尾至多 n 字节（截断长日志便于阅读）。
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "...(truncated)...\n" + s[len(s)-n:]
}
