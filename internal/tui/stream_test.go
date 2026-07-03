package tui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/alaindong/cogent/internal/types"
)

func TestDiffStat(t *testing.T) {
	diff := "--- a\n+++ b\n@@ -1,2 +1,3 @@\n-old\n+new1\n+new2\n unchanged\n"
	add, del := diffStat(diff)
	if add != 2 || del != 1 {
		t.Fatalf("diffStat add=%d del=%d want add=2 del=1", add, del)
	}
}

func TestResultStatus(t *testing.T) {
	cases := []struct {
		name string
		res  types.ToolResult
		want string
	}{
		{"diff", types.ToolResult{Diff: "+++ a\n+x\n-y\n"}, "+1"},
		{"error", types.ToolResult{IsError: true, Content: "boom failed"}, "boom failed"},
		{"multiline", types.ToolResult{Content: "l1\nl2\nl3"}, "3 lines"},
		{"single line", types.ToolResult{Content: "just one"}, "just one"},
		{"empty", types.ToolResult{Content: ""}, "done"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resultStatus(&c.res)
			if !strings.Contains(got, c.want) {
				t.Fatalf("resultStatus=%q want contains %q", got, c.want)
			}
		})
	}
}

// TestStreamPlain_ToolFoldsToSummary 验证非 TTY 路径下工具调用折叠为单行摘要、
// 结果收敛为规模而非全量刷屏，且工具区间内的进度文本被抑制。
func TestStreamPlain_ToolFoldsToSummary(t *testing.T) {
	var buf bytes.Buffer
	r := newStreamRenderer(&buf, false, "cogent> ")
	big := strings.Repeat("line\n", 500)
	events := []types.StreamEvent{
		{Type: types.EventText, Text: "hello "},
		{Type: types.EventToolStart, ToolUse: &types.ToolUseBlock{Name: "read_file", Input: []byte(`{"path":"x.go"}`)}},
		{Type: types.EventText, Text: "noisy progress that must be suppressed"},
		{Type: types.EventToolResult, Result: &types.ToolResult{Content: big}},
		{Type: types.EventDone},
	}
	for _, ev := range events {
		if err := r.handle(ev); err != nil {
			t.Fatalf("handle: %v", err)
		}
	}
	out := buf.String()
	if !strings.Contains(out, "[tool] read_file x.go") {
		t.Errorf("missing tool summary line, got:\n%s", out)
	}
	if !strings.Contains(out, "500 lines") {
		t.Errorf("result should collapse to line count, got:\n%s", out)
	}
	if strings.Contains(out, "noisy progress") {
		t.Errorf("in-tool progress text must be suppressed, got:\n%s", out)
	}
	if strings.Count(out, "line\nline\n") > 0 {
		t.Errorf("raw result content must not be dumped, got %d bytes", len(out))
	}
}

// TestStreamRich_ToolSummaryLine 验证 TTY 富渲染下工具调用输出 `● 工具名 摘要`
// 与结果状态行 `⎿`。
func TestStreamRich_ToolSummaryLine(t *testing.T) {
	var buf bytes.Buffer
	r := &streamRenderer{w: &buf, rich: true, promptShown: true} // 跳过 spinner
	_ = r.handle(types.StreamEvent{Type: types.EventToolStart,
		ToolUse: &types.ToolUseBlock{Name: "bash", Input: []byte(`{"command":"ls"}`)}})
	_ = r.handle(types.StreamEvent{Type: types.EventToolResult,
		Result: &types.ToolResult{Content: "a\nb"}})
	out := buf.String()
	if !strings.Contains(out, "● ") || !strings.Contains(out, "bash ls") {
		t.Errorf("missing bullet tool summary, got:\n%q", out)
	}
	if !strings.Contains(out, "⎿") {
		t.Errorf("missing result status marker, got:\n%q", out)
	}
}
