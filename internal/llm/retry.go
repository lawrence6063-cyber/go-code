// Package llm 中的 retry.go 提供建流阶段的可重试错误判定与指数退避（含 jitter）。
// 仅用标准库实现，不引入第三方退避库；零值 RetryPolicy 表示不重试（向后兼容）。
package llm

import (
	"context"
	"errors"
	"math/rand"
	"net"
	"syscall"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

// RetryPolicy 配置可重试错误的退避策略；零值（MaxAttempts<=1）表示不重试，保持向后兼容。
type RetryPolicy struct {
	MaxAttempts int           // 最大尝试次数（含首次）；<=1 即不重试
	BaseDelay   time.Duration // 首次退避基准
	MaxDelay    time.Duration // 退避上限
}

// retryDefaults 是退避基准/上限的兜底值，仅在策略开启重试但未配置时使用。
const (
	defaultBaseDelay = 500 * time.Millisecond
	defaultMaxDelay  = 10 * time.Second
)

// enabled 报告该策略是否真的会发起重试。
func (p RetryPolicy) enabled() bool { return p.MaxAttempts > 1 }

// backoff 计算第 attempt 次重试（attempt 从 1 起）的退避时长：指数增长并叠加 [0,base) 的 jitter，受 MaxDelay 封顶。
func (p RetryPolicy) backoff(attempt int) time.Duration {
	base := p.BaseDelay
	if base <= 0 {
		base = defaultBaseDelay
	}
	maxDelay := p.MaxDelay
	if maxDelay <= 0 {
		maxDelay = defaultMaxDelay
	}
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= maxDelay {
			d = maxDelay
			break
		}
	}
	jitter := time.Duration(rand.Int63n(int64(base))) //nolint:gosec // 退避抖动无需密码学随机
	d += jitter
	if d > maxDelay {
		d = maxDelay
	}
	return d
}

// sleep 在退避时长内等待，期间尊重 ctx 取消：被取消时立即返回 ctx 错误。
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// isRetryable 仅对限流/服务端/网络瞬时错误返回 true；ctx 取消与一般 4xx（除 429）不重试。
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		return retryableStatus(apiErr.HTTPStatusCode)
	}
	var reqErr *openai.RequestError
	if errors.As(err, &reqErr) {
		if retryableStatus(reqErr.HTTPStatusCode) {
			return true
		}
		return isRetryable(reqErr.Err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return errors.Is(err, syscall.ECONNRESET)
}

// retryableStatus 报告 HTTP 状态码是否属于可重试类别：429 限流或 5xx 服务端错误。
func retryableStatus(code int) bool {
	return code == 429 || (code >= 500 && code <= 599)
}
