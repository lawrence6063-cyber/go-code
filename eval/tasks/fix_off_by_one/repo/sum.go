// Package mathx 提供基础数值聚合函数。
package mathx

// SumTo 返回 1..n 的累加和（闭区间，含 n）。
// 当前实现存在 off-by-one 缺陷：循环漏掉了 n 本身，需修复为闭区间求和。
func SumTo(n int) int {
	total := 0
	for i := 1; i < n; i++ {
		total += i
	}
	return total
}
