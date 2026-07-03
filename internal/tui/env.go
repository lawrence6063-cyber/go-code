package tui

import (
	"os"
	"strconv"
	"strings"
)

// envFloat 读取浮点环境变量，缺省或非法时回退 def（供成本单价覆盖等使用）。
func envFloat(key string, def float64) float64 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}
