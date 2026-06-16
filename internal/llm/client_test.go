package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alaindong/cogent/internal/types"
)

// newSSEServer 启动一个返回固定 OpenAI 兼容 SSE 流的测试服务器。
func newSSEServer(t *testing.T, chunks []string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("response writer is not a flusher")
			return
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			flusher.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestClient_StreamParsesSSE(t *testing.T) {
	chunks := []string{
		`{"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":" world"}}]}`,
		`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2}}`,
		"[DONE]",
	}
	srv := newSSEServer(t, chunks)

	c, err := New(Config{APIKey: "test-key", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	deltas, err := c.Stream(context.Background(), Request{
		Model:    "deepseek-chat",
		Messages: []types.Message{{Role: types.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var text string
	var usage *Usage
	for d := range deltas {
		if d.Err != nil {
			t.Fatalf("delta error: %v", d.Err)
		}
		text += d.Text
		if d.Usage != nil {
			usage = d.Usage
		}
	}
	if text != "Hello world" {
		t.Errorf("text = %q, want %q", text, "Hello world")
	}
	if usage == nil {
		t.Fatal("usage = nil, want populated")
	}
	if usage.PromptTokens != 10 || usage.CompletionTokens != 2 {
		t.Errorf("usage = %+v, want {10 2}", *usage)
	}
}

func TestClient_StreamCtxCancel(t *testing.T) {
	srv := newSSEServer(t, []string{
		`{"choices":[{"index":0,"delta":{"content":"x"}}]}`,
		"[DONE]",
	})
	c, err := New(Config{APIKey: "test-key", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	deltas, err := c.Stream(ctx, Request{Model: "m", Messages: []types.Message{{Role: types.RoleUser, Text: "hi"}}})
	if err != nil {
		// 取消可能在创建流时即返回错误，亦为可接受路径。
		return
	}
	// 否则 channel 应能正常排空并关闭，不挂起。
	for range deltas { //nolint:revive // 仅排空
	}
}

func TestClient_StreamAccumulatesToolCalls(t *testing.T) {
	// 一次 tool_call 的 arguments 跨多帧分片到达，末帧 finish_reason=tool_calls。
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"pa"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a.go\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3}}`,
		"[DONE]",
	}
	srv := newSSEServer(t, chunks)
	c, err := New(Config{APIKey: "test-key", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	deltas, err := c.Stream(context.Background(), Request{
		Model:    "deepseek-chat",
		Messages: []types.Message{{Role: types.RoleUser, Text: "read it"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var calls []types.ToolUseBlock
	for d := range deltas {
		if d.Err != nil {
			t.Fatalf("delta error: %v", d.Err)
		}
		if d.ToolCall != nil {
			calls = append(calls, *d.ToolCall)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(calls))
	}
	got := calls[0]
	if got.ID != "call_1" || got.Name != "read_file" {
		t.Errorf("tool call = {ID:%q Name:%q}, want {call_1 read_file}", got.ID, got.Name)
	}
	if want := `{"path":"a.go"}`; string(got.Input) != want {
		t.Errorf("tool call input = %q, want %q", string(got.Input), want)
	}
}

func TestNew_MissingAPIKey(t *testing.T) {
	if _, err := New(Config{APIKey: ""}); err == nil {
		t.Error("expected error for missing api key, got nil")
	}
}

func TestNew_DefaultBaseURL(t *testing.T) {
	// 仅验证构造成功（不发请求）；BaseURL 为空时使用 go-openai 默认。
	if _, err := New(Config{APIKey: "k"}); err != nil {
		t.Errorf("New with default base url: %v", err)
	}
}
