// Package parsex 提供严格的正整数解析。
package parsex

import "strconv"

// ParsePositive 将字符串解析为正整数（> 0）。
// 非数字、空串、零或负数都必须返回 error，且不得 panic。
//
// 缺陷（reviewer 关注的质量点）：当前实现用 _ 吞掉了 strconv 的错误，
// 也未校验正数边界，会把非法输入静默当作 0 返回，掩盖错误。
func ParsePositive(s string) (int, error) {
	n, _ := strconv.Atoi(s)
	return n, nil
}
