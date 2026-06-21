#!/usr/bin/env bash
# verify.sh — finish_reason 自循环任务的客观判据（退出码 0 = 达标）。
# 遵循 eval/README 约定：初始状态（Delta 无 FinishReason 字段）必失败；loop 实现后才通过。
# 设计要点（防自评虚高）：在校验时即时材料化一份独立验收测试，跑完即删，
# 降低被 loop 篡改 verify 的风险；判定完全基于客观行为（注入 SSE → 断言 Delta.FinishReason）。
set -uo pipefail

# 从脚本自身位置上溯到仓库根（eval/tasks/finish_reason_selfloop/verify.sh → 上溯 3 级）。
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
cd "$ROOT" || { echo "FAIL: cannot cd to repo root"; exit 1; }

ACCEPT="$ROOT/internal/llm/zz_finish_reason_acceptance_test.go"
cleanup() { rm -f "$ACCEPT"; }
trap cleanup EXIT

# 1) 即时材料化独立验收测试（package llm 内部测试，复用 client_test.go 的 newSSEServer）。
cat > "$ACCEPT" <<'EOF'
package llm

import (
	"context"
	"testing"

	"github.com/alaindong/cogent/internal/types"
)

// TestZZFinishReasonSurfaced 注入含 finish_reason 的 SSE 流，断言 Delta 透出了 FinishReason。
func TestZZFinishReasonSurfaced(t *testing.T) {
	srv := newSSEServer(t, []string{
		`{"choices":[{"index":0,"delta":{"content":"hi"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		"[DONE]",
	})
	c, err := New(Config{APIKey: "k", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	deltas, err := c.Stream(context.Background(), Request{
		Model:    "m",
		Messages: []types.Message{{Role: types.RoleUser, Text: "x"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var got string
	for d := range deltas {
		if d.FinishReason != "" {
			got = d.FinishReason
		}
	}
	if got != "stop" {
		t.Fatalf("Delta.FinishReason not surfaced: got %q, want %q", got, "stop")
	}
}
EOF
gofmt -w "$ACCEPT"

# 2) 格式 / 静态检查 / 编译。
echo "== gofmt =="
if [ -n "$(gofmt -l internal)" ]; then
	echo "FAIL: gofmt found unformatted files:"; gofmt -l internal; exit 1
fi
echo "== go vet =="
go vet ./... || { echo "FAIL: go vet"; exit 1; }
echo "== go build =="
go build ./... || { echo "FAIL: go build"; exit 1; }

# 3) 结构检查：engine 必须写入 llm.finish_reason span 属性。
echo "== engine wires llm.finish_reason =="
if ! grep -rq 'llm.finish_reason' internal/engine/; then
	echo "FAIL: internal/engine missing llm.finish_reason span attribute"; exit 1
fi

# 4) 跑 llm + engine 测试（含即时材料化的验收测试）。
echo "== go test (llm + engine, incl acceptance) =="
go test ./internal/llm/... ./internal/engine/... || { echo "FAIL: go test"; exit 1; }

echo "ALL PASS: finish_reason surfaced end-to-end"
exit 0
