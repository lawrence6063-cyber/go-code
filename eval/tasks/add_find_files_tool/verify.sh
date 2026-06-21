#!/usr/bin/env bash
# verify.sh —— add_find_files_tool 任务客观判据（在 maker 改动所在目录 / worktree 根执行）。
# 退出码 0 = 目标达成。组合判据缺一不可：编译 + 注册校验 + 注入式行为校验(canary) + 全量测试。
# 客观性：注册校验与 canary 行为测试锁死 find_files 契约，空实现/漏注册都会被拒。
set -euo pipefail

# 定位代码根：优先当前工作目录（loop 在 worktree 根执行 verify）；
# 否则回退到脚本相对仓库根（供本地基线手动校验：eval/tasks/add_find_files_tool/ -> 上三级）。
root="$(pwd)"
if [ ! -f "${root}/go.mod" ]; then
	root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
fi
cd "${root}"

fail() {
	echo "FAIL: add_find_files_tool — $1"
	exit 1
}

# (1) 编译全仓库
go build ./... || fail "go build ./... 失败"

# (2) 注册校验：find_files 必须在 4 处只读池装配点全部注册（commands.go 中 NewFindFiles 出现 >= 4 次）
reg_count="$(grep -c 'NewFindFiles' cmd/cogent/commands.go || true)"
if [ "${reg_count}" -lt 4 ]; then
	fail "commands.go 中 NewFindFiles 注册 ${reg_count} 处 (<4)：需在 buildToolPool/buildMakerPool/buildSpawner/buildReviewerPool 全部注册"
fi

# (3) 注入式行为校验：写入临时 canary 测试锁死 find_files 契约，跑完即删（trap 兜底）
canary="internal/tool/zz_findfiles_canary_test.go"
cleanup() { rm -f "${canary}"; }
trap cleanup EXIT
cat > "${canary}" <<'EOF'
package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindFilesCanary(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "alpha.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "beta.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tl := NewFindFiles(dir)
	if tl.Name() != "find_files" {
		t.Fatalf("name = %q, want find_files", tl.Name())
	}
	if !tl.IsReadOnly(nil) || !tl.IsConcurrencySafe(nil) {
		t.Fatal("find_files must be read-only and concurrency-safe")
	}
	res, _ := tl.Call(context.Background(), json.RawMessage(`{"pattern":"*.go"}`), nil)
	if res.IsError {
		t.Fatalf("call returned error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "alpha.go") || !strings.Contains(res.Content, "beta.go") {
		t.Fatalf("expected alpha.go and beta.go (recursive), got: %q", res.Content)
	}
	if strings.Contains(res.Content, "note.txt") {
		t.Fatalf("pattern *.go must not match note.txt, got: %q", res.Content)
	}
	res, _ = tl.Call(context.Background(), json.RawMessage(`{"pattern":"*","path":"../escape"}`), nil)
	if !res.IsError {
		t.Fatal("expected out-of-workspace path to error")
	}
}
EOF

# (4) 全量测试（含 canary 与 maker 自带测试）
go test ./... || fail "go test ./... 失败（含 find_files 契约 canary）"

echo "PASS: add_find_files_tool"
exit 0
