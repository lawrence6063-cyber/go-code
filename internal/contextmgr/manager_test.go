package contextmgr

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/alaindong/cogent/internal/llm"
	"github.com/alaindong/cogent/internal/types"
)

// TestMain 在包级别断言无 goroutine 泄漏（spec §9.3）。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fakeLLM 是 llm.Client 的测试替身：返回固定摘要文本，或注入一个流内错误。
type fakeLLM struct {
	text    string
	fail    bool
	lastReq llm.Request // 记录最近一次请求，供断言摘要预算/温度
}

func (f *fakeLLM) Stream(_ context.Context, req llm.Request) (<-chan llm.Delta, error) {
	f.lastReq = req
	out := make(chan llm.Delta, 2)
	go func() {
		defer close(out)
		if f.fail {
			out <- llm.Delta{Err: errors.New("boom")}
			return
		}
		out <- llm.Delta{Text: f.text}
	}()
	return out, nil
}

// pairedHistory 构造一段含 tool_use/tool_result 配对的消息历史。
func pairedHistory() []types.Message {
	return []types.Message{
		{Role: types.RoleSystem, Text: "sys"},
		{Role: types.RoleUser, Text: "aaaa bbbb cccc"},
		{Role: types.RoleAssistant, ToolCalls: []types.ToolUseBlock{{ID: "c1", Name: "read_file"}}},
		{Role: types.RoleTool, ToolUseID: "c1", Text: "file body"},
		{Role: types.RoleUser, Text: "second question"},
	}
}

func TestManager_ShouldCompact(t *testing.T) {
	t.Setenv(envWindow, "1000")
	t.Setenv(envReserve, "200")
	t.Setenv(envBuffer, "100")
	t.Setenv(envKeep, "50")
	m := New() // effectiveWindow=800，触发条件 used+100>=800 → used>=700

	cases := []struct {
		name string
		used int
		want bool
	}{
		{"far below", 0, false},
		{"just below", 699, false},
		{"at threshold", 700, true},
		{"above", 1200, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := m.ShouldCompact(tc.used, "test-model"); got != tc.want {
				t.Errorf("ShouldCompact(%d) = %v, want %v", tc.used, got, tc.want)
			}
		})
	}
}

func TestManager_CircuitBreaker(t *testing.T) {
	t.Setenv(envKeep, "1") // 让切点落在历史中部，确保会真正尝试摘要
	m := New()
	f := &fakeLLM{fail: true}
	msgs := []types.Message{
		{Role: types.RoleSystem, Text: "sys"},
		{Role: types.RoleUser, Text: "uuuu"},
		{Role: types.RoleAssistant, Text: "aaaa"},
		{Role: types.RoleUser, Text: "eeee"},
	}
	ctx := context.Background()

	if _, err := m.Compact(ctx, msgs, f); err == nil || errors.Is(err, ErrCompactGiveUp) {
		t.Fatalf("first failure err = %v, want a non-giveup error", err)
	}
	if _, err := m.Compact(ctx, msgs, f); err == nil || errors.Is(err, ErrCompactGiveUp) {
		t.Fatalf("second failure err = %v, want a non-giveup error", err)
	}
	if _, err := m.Compact(ctx, msgs, f); !errors.Is(err, ErrCompactGiveUp) {
		t.Fatalf("third failure err = %v, want ErrCompactGiveUp", err)
	}
	if m.ShouldCompact(1<<30, "test-model") {
		t.Error("ShouldCompact should return false after circuit opens")
	}
}

func TestManager_Compact_Success(t *testing.T) {
	t.Setenv(envKeep, "1")
	m := New()
	f := &fakeLLM{text: "SUMMARY-X"}
	msgs := []types.Message{
		{Role: types.RoleSystem, Text: "sys"},
		{Role: types.RoleUser, Text: "first question text"},
		{Role: types.RoleAssistant, Text: "first reply text"},
		{Role: types.RoleUser, Text: "latest question"},
	}

	out, err := m.Compact(context.Background(), msgs, f)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("rebuilt len = %d, want 3: %+v", len(out), out)
	}
	if out[0].Role != types.RoleSystem {
		t.Errorf("out[0].Role = %s, want system", out[0].Role)
	}
	if out[1].Role != types.RoleUser || !strings.Contains(out[1].Text, "SUMMARY-X") {
		t.Errorf("out[1] should be user summary containing SUMMARY-X, got %+v", out[1])
	}
	if out[2].Text != "latest question" {
		t.Errorf("tail message text = %q, want latest question", out[2].Text)
	}
}

func TestManager_Compact_NoHistory(t *testing.T) {
	m := New()
	msgs := []types.Message{{Role: types.RoleSystem, Text: "sys"}}
	out, err := m.Compact(context.Background(), msgs, &fakeLLM{text: "x"})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(out) != 1 {
		t.Errorf("len = %d, want 1 (nothing to compact)", len(out))
	}
}

func TestAdjustForPairing(t *testing.T) {
	msgs := pairedHistory()
	// 切点落在 tool_result（index 3）上：应向头部平移到其配对的 assistant（index 2）。
	if got := adjustForPairing(msgs, 3); got != 2 {
		t.Errorf("adjustForPairing(_,3) = %d, want 2", got)
	}
	// 切点落在普通 user（index 4）上：无需平移。
	if got := adjustForPairing(msgs, 4); got != 4 {
		t.Errorf("adjustForPairing(_,4) = %d, want 4", got)
	}
	// 不得越过系统提示。
	if got := adjustForPairing(msgs, 1); got != 1 {
		t.Errorf("adjustForPairing(_,1) = %d, want 1", got)
	}
}

func TestManager_Compact_PreservesPairing(t *testing.T) {
	t.Setenv(envKeep, "2") // 让切点尽量靠尾部，可能落在 tool_result 上
	m := New()
	out, err := m.Compact(context.Background(), pairedHistory(), &fakeLLM{text: "S"})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	// 重建后的尾部不得以孤立的 tool_result 开头（否则破坏 function calling 配对）。
	for i := 1; i < len(out); i++ {
		if out[i].Role == types.RoleTool {
			if out[i-1].Role != types.RoleAssistant || len(out[i-1].ToolCalls) == 0 {
				t.Fatalf("orphan tool_result at %d, preceding = %+v", i, out[i-1])
			}
		}
	}
}

func TestEstimateTokens(t *testing.T) {
	msgs := []types.Message{{Role: types.RoleUser, Text: "12345678"}} // 8 runes / 4 = 2
	if got := EstimateTokens(msgs); got != 2 {
		t.Errorf("EstimateTokens = %d, want 2", got)
	}
}

func TestManager_Compact_SummaryRequestBudget(t *testing.T) {
	t.Setenv(envReserve, "1234") // 摘要预留应作为摘要调用的 MaxTokens 上限
	t.Setenv(envKeep, "1")       // 切点落在历史中部，确保真正发起摘要调用
	m := New()
	f := &fakeLLM{text: "S"}
	msgs := []types.Message{
		{Role: types.RoleSystem, Text: "sys"},
		{Role: types.RoleUser, Text: "first question"},
		{Role: types.RoleAssistant, Text: "first reply"},
		{Role: types.RoleUser, Text: "latest question"},
	}

	if _, err := m.Compact(context.Background(), msgs, f); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if f.lastReq.MaxTokens != 1234 {
		t.Errorf("summary MaxTokens = %d, want 1234 (= ReservedForSummary)", f.lastReq.MaxTokens)
	}
	if f.lastReq.Temperature != summaryTemperature {
		t.Errorf("summary Temperature = %v, want %v", f.lastReq.Temperature, summaryTemperature)
	}
}
