package serveapp

import (
	"fmt"
	"net/http"
)

// NewHandler 返回问候服务的 HTTP 处理器：
// GET /greet?name=X 应返回状态 200、响应体 "hello, X"（name 缺省为 "world"）。
func NewHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/greet", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			name = "world"
		}
		// BUG: 未按要求返回 "hello, <name>"，而是写了占位响应
		fmt.Fprint(w, "TODO")
	})
	return mux
}
