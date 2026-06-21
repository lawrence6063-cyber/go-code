package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	openai "github.com/sashabaranov/go-openai"

	"github.com/alaindong/cogent/internal/types"
)

// timeoutErr 是一个仅实现 net.Error 且 Timeout()=true 的测试错误。
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

var _ net.Error = timeoutErr{}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"ctx canceled", context.Canceled, false},
		{"ctx deadline", context.DeadlineExceeded, false},
		{"api 429", &openai.APIError{HTTPStatusCode: 429}, true},
		{"api 503", &openai.APIError{HTTPStatusCode: 503}, true},
		{"api 400", &openai.APIError{HTTPStatusCode: 400}, false},
		{"api 401", &openai.APIError{HTTPStatusCode: 401}, false},
		{"request 500", &openai.RequestError{HTTPStatusCode: 500}, true},
		{"request wraps timeout", &openai.RequestError{HTTPStatusCode: 0, Err: timeoutErr{}}, true},
		{"net timeout", timeoutErr{}, true},
		{"conn reset", syscall.ECONNRESET, true},
		{"plain error", errors.New("boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryable(tt.err); got != tt.want {
				t.Errorf("isRetryable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestRetryPolicy_Enabled(t *testing.T) {
	if (RetryPolicy{}).enabled() {
		t.Error("zero policy should be disabled (no retry)")
	}
	if (RetryPolicy{MaxAttempts: 1}).enabled() {
		t.Error("MaxAttempts=1 should be disabled")
	}
	if !(RetryPolicy{MaxAttempts: 3}).enabled() {
		t.Error("MaxAttempts=3 should be enabled")
	}
}

func TestRetryPolicy_BackoffGrowsAndCaps(t *testing.T) {
	p := RetryPolicy{MaxAttempts: 5, BaseDelay: 100 * time.Millisecond, MaxDelay: 400 * time.Millisecond}
	// 退避按指数增长（含 [0,base) jitter），且不超过 MaxDelay+base 的合理上界。
	d1 := p.backoff(1)
	d3 := p.backoff(3)
	if d1 < 100*time.Millisecond {
		t.Errorf("backoff(1) = %v, want >= base", d1)
	}
	if d3 > p.MaxDelay {
		t.Errorf("backoff(3) = %v, want <= MaxDelay %v", d3, p.MaxDelay)
	}
}

func TestSleep_RespectsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleep(ctx, time.Hour); err == nil {
		t.Error("sleep should return ctx error when canceled")
	}
}

// TestClient_StreamRetriesOn429 验证建流阶段遇 429 会退避重试，最终成功。
func TestClient_StreamRetriesOn429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"rate limited","type":"rate_limit"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)

	c, err := New(Config{
		APIKey:  "k",
		BaseURL: srv.URL,
		Retry:   RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	deltas, err := c.Stream(context.Background(), Request{Model: "m", Messages: []types.Message{{Role: types.RoleUser, Text: "hi"}}})
	if err != nil {
		t.Fatalf("Stream after retry: %v", err)
	}
	var text string
	for d := range deltas {
		if d.Err != nil {
			t.Fatalf("delta error: %v", d.Err)
		}
		text += d.Text
	}
	if text != "ok" {
		t.Errorf("text = %q, want %q", text, "ok")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("server calls = %d, want 2 (1 fail + 1 retry success)", got)
	}
}

// TestClient_StreamNoRetryWithoutPolicy 验证零值策略下 429 不重试，直接失败。
func TestClient_StreamNoRetryWithoutPolicy(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"rate limited"}}`)
	}))
	t.Cleanup(srv.Close)

	c, _ := New(Config{APIKey: "k", BaseURL: srv.URL}) // 无 Retry
	if _, err := c.Stream(context.Background(), Request{Model: "m", Messages: []types.Message{{Role: types.RoleUser, Text: "hi"}}}); err == nil {
		t.Fatal("expected error without retry policy")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server calls = %d, want 1 (no retry)", got)
	}
}
