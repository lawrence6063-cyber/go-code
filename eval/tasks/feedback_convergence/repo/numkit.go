// Package numkit 提供整数切片的基础统计函数。
package numkit

import "errors"

// ErrEmpty 表示对空切片求统计值。
var ErrEmpty = errors.New("empty input")

// Max 返回切片中的最大值；空切片返回 ErrEmpty。
// 缺陷：以 0 作为初值，全负数切片会错误返回 0。
func Max(xs []int) (int, error) {
	if len(xs) == 0 {
		return 0, ErrEmpty
	}
	max := 0
	for _, x := range xs {
		if x > max {
			max = x
		}
	}
	return max, nil
}

// Mean 返回向零截断的算术平均；空切片返回 ErrEmpty。
// 缺陷：未处理空切片，len(xs)==0 时会触发除零 panic。
func Mean(xs []int) (int, error) {
	sum := 0
	for _, x := range xs {
		sum += x
	}
	return sum / len(xs), nil
}
