#!/usr/bin/env bash
# verify.sh —— session_root_span 任务客观判据（退出码 0 = 达标）。
# 判定逻辑：注入式验收测试用真实 file exporter 构造 engine，跑一次 Run，
# 从 trace 文件中断言 cogent.session span 存在且是 react.step 的父 span。
# 防自评虚高：验收测试即时材料化，跑完即删；判定完全基于客观 trace 结构。
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
cd "$ROOT" || { echo "FAIL: cannot cd to repo root"; exit 1; }

ACCEPT="$ROOT/internal/engine/zz_session_span_acceptance_test.go"
cleanup() { rm -f "$ACCEPT"; }
trap cleanup EXIT

# 1) 即时材料化独立验收测试（engine 包内部测试，复用 fakeLLM）。
cat > "$ACCEPT" <<'EOF'
package engine

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/alaindong/cogent/internal/llm"
	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/types"
)

// spanRecord 是 trace jsonl 中单条 span 的最小投影。
type spanRecord struct {
	Name        string `json:"Name"`
	SpanContext struct {
		TraceID string `json:"TraceID"`
		SpanID  string `json:"SpanID"`
	} `json:"SpanContext"`
	Parent struct {
		TraceID string `json:"TraceID"`
		SpanID  string `json:"SpanID"`
	} `json:"Parent"`
}

// TestZZSessionRootSpan 用真实 file exporter 跑一次 engine.Run，
// 断言 trace 中存在 cogent.session span 且 react.step 的 parent 指向它。
func TestZZSessionRootSpan(t *testing.T) {
	dir := t.TempDir()
	prov, err := observe.New(observe.Config{
		Enabled:     true,
		Exporter:    "file",
		TraceDir:    dir,
		SampleRatio: 1.0,
	})
	if err != nil {
		t.Fatalf("observe.New: %v", err)
	}

	// fakeLLM：单轮纯对话，直接返回文本 + usage 后结束（触发 EventDone）。
	f := &fakeLLM{turns: [][]llm.Delta{{
		{Text: "ok"},
		{Usage: &llm.Usage{PromptTokens: 5, CompletionTokens: 1}},
	}}}
	eng, err := New(Deps{LLM: f, Observe: prov, Model: "test-session"})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}

	events, err := eng.Run(context.Background(), "do something")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for ev := range events {
		_ = ev
	}

	if err := prov.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	spans := readTraceSpans(t, dir)
	var sessionSpan, stepSpan *spanRecord
	for i := range spans {
		switch spans[i].Name {
		case "cogent.session":
			sessionSpan = &spans[i]
		case "react.step":
			stepSpan = &spans[i]
		}
	}
	if sessionSpan == nil {
		t.Fatalf("cogent.session span not found in trace (spans: %d)", len(spans))
	}
	if stepSpan == nil {
		t.Fatalf("react.step span not found in trace (spans: %d)", len(spans))
	}
	// react.step 必须挂在 cogent.session 之下（同一 trace，parent 指向 session）。
	if stepSpan.SpanContext.TraceID != sessionSpan.SpanContext.TraceID {
		t.Errorf("trace_id mismatch: session=%s step=%s",
			sessionSpan.SpanContext.TraceID, stepSpan.SpanContext.TraceID)
	}
	if stepSpan.Parent.SpanID != sessionSpan.SpanContext.SpanID {
		t.Errorf("react.step parent_span=%s, want cogent.session span=%s",
			stepSpan.Parent.SpanID, sessionSpan.SpanContext.SpanID)
	}
}

func readTraceSpans(t *testing.T, dir string) []spanRecord {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "traces-*.jsonl"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no trace file in %s (err=%v)", dir, err)
	}
	f, err := os.Open(matches[0])
	if err != nil {
		t.Fatalf("open trace: %v", err)
	}
	defer func() { _ = f.Close() }()
	var spans []spanRecord
	dec := json.NewDecoder(f)
	for {
		var s spanRecord
		if derr := dec.Decode(&s); derr == io.EOF {
			break
		} else if derr != nil {
			t.Fatalf("decode span: %v", derr)
		}
		spans = append(spans, s)
	}
	return spans
}
EOF
gofmt -w "$ACCEPT"

# 2) 结构检查：engine 源码中必须出现 cogent.session span 名（排除即时验收测试文件）。
echo "== structural check: cogent.session span =="
if ! grep -rq '"cogent.session"' internal/engine/ --exclude='zz_*'; then
	echo "FAIL: internal/engine missing \"cogent.session\" span"; exit 1
fi

# 3) 格式 / 静态检查 / 编译（仅检查 engine 包，避免预存格式问题干扰）。
echo "== gofmt =="
if [ -n "$(gofmt -l internal/engine)" ]; then
	echo "FAIL: gofmt found unformatted files:"; gofmt -l internal/engine; exit 1
fi
echo "== go vet =="
go vet ./... || { echo "FAIL: go vet"; exit 1; }
echo "== go build =="
go build ./... || { echo "FAIL: go build"; exit 1; }

# 4) 跑 engine 测试（含即时材料化的验收测试）。
echo "== go test (engine, incl acceptance) =="
go test ./internal/engine/... || { echo "FAIL: go test"; exit 1; }

# 5) 全量测试回归（确保改动不破坏其它包）。
echo "== go test ./... =="
go test ./... || { echo "FAIL: go test ./..."; exit 1; }

echo "ALL PASS: cogent.session root span surfaced"
exit 0
