package serveapp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGreet 用 httptest 起真实回环 HTTP 服务，做真实客户端往返，断言状态码与响应体。
func TestGreet(t *testing.T) {
	srv := httptest.NewServer(NewHandler())
	defer srv.Close()

	cases := []struct {
		url  string
		want string
	}{
		{srv.URL + "/greet?name=cogent", "hello, cogent"},
		{srv.URL + "/greet", "hello, world"},
	}
	for _, tc := range cases {
		resp, err := http.Get(tc.url)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.url, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", tc.url, resp.StatusCode)
		}
		if string(body) != tc.want {
			t.Errorf("GET %s body = %q, want %q", tc.url, string(body), tc.want)
		}
	}
}
