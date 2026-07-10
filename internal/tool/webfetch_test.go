// Package tool 中的 webfetch_test.go 覆盖 web_fetch 的 SSRF 防护与内容渲染逻辑。
package tool

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alaindong/cogent/internal/permission"
)

func TestIsBlockedIP(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{"loopback v4", "127.0.0.1", true},
		{"loopback v6", "::1", true},
		{"private 10", "10.1.2.3", true},
		{"private 172.16", "172.16.0.5", true},
		{"private 192.168", "192.168.1.1", true},
		{"link local", "169.254.1.1", true},
		{"unspecified", "0.0.0.0", true},
		{"extra block 9", "9.9.9.9", true},
		{"extra block 11", "11.0.0.1", true},
		{"extra block 21", "21.1.1.1", true},
		{"extra block 30", "30.1.1.1", true},
		{"ipv6 unique local", "fc00::1", true},
		{"ipv6 link local", "fe80::1", true},
		{"public dns", "8.8.8.8", false},
		{"public generic", "203.0.113.5", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBlockedIP(net.ParseIP(tt.ip)); got != tt.want {
				t.Errorf("isBlockedIP(%q) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestValidateFetchURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"http ok", "http://example.com/page", false},
		{"https ok", "https://example.com", false},
		{"ftp rejected", "ftp://example.com/file", true},
		{"file scheme rejected", "file:///etc/passwd", true},
		{"no host", "http:///path", true},
		{"malformed", "http://[::1", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFetchURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateFetchURL(%q) err = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

// TestWebFetch_BlocksLoopbackTarget 端到端验证：即便目标是一个真实可达的本地 HTTP 服务，
// 只要落在回环地址段就必须被拒绝——这是 SSRF 防护要拦截的核心场景（内网/本机服务被诱导访问）。
func TestWebFetch_BlocksLoopbackTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("should never be reached"))
	}))
	defer srv.Close()

	tl := NewWebFetch(testTracer())
	res, _ := tl.Call(context.Background(), mustJSON(t, map[string]string{"url": srv.URL}), nil)
	if !res.IsError {
		t.Fatalf("expected loopback target to be blocked, got %+v", res)
	}
	if !strings.Contains(res.Content, "blocked") {
		t.Errorf("error content = %q, want mention of blocked", res.Content)
	}
}

// TestWebFetchTool_CheckPermission 覆盖 CheckPermission 阶段的静态 scheme/host 校验。
func TestWebFetchTool_CheckPermission(t *testing.T) {
	tl := NewWebFetch(testTracer())

	dec, _ := tl.CheckPermission(context.Background(), mustJSON(t, map[string]string{"url": "ftp://example.com"}))
	if dec.Behavior != permission.BehaviorDeny {
		t.Errorf("ftp scheme behavior = %v, want deny", dec.Behavior)
	}

	dec, _ = tl.CheckPermission(context.Background(), mustJSON(t, map[string]string{"url": "https://example.com"}))
	if dec.Behavior != permission.BehaviorAsk {
		t.Errorf("normal https behavior = %v, want ask", dec.Behavior)
	}
}

func TestRenderFetchBody(t *testing.T) {
	html := `<html><head><style>body{color:red}</style></head><body><p>Hello <b>World</b></p><script>evil()</script></body></html>`
	got := renderFetchBody([]byte(html), "text/html; charset=utf-8")
	if strings.Contains(got, "evil()") || strings.Contains(got, "color:red") {
		t.Errorf("script/style content leaked into rendered body: %q", got)
	}
	if !strings.Contains(got, "Hello") || !strings.Contains(got, "World") {
		t.Errorf("rendered body missing visible text: %q", got)
	}
	if strings.Contains(got, "<") {
		t.Errorf("rendered body still contains raw tag markers: %q", got)
	}

	plain := renderFetchBody([]byte("just plain text"), "text/plain")
	if plain != "just plain text" {
		t.Errorf("plain text should pass through unchanged, got %q", plain)
	}

	big := strings.Repeat("x", maxFetchBytes+100)
	truncatedOut := renderFetchBody([]byte(big), "text/plain")
	if !strings.HasSuffix(truncatedOut, "[truncated]") {
		t.Errorf("oversized body should be truncated with marker, got suffix %q", truncatedOut[len(truncatedOut)-30:])
	}
}
