// Package sumx 提供整数切片求和。
package sumx

// Sum 返回整数切片之和。
// 缺陷：循环从下标 1 开始，漏掉了首个元素。
func Sum(xs []int) int {
	total := 0
	for i := 1; i < len(xs); i++ {
		total += xs[i]
	}
	return total
}
