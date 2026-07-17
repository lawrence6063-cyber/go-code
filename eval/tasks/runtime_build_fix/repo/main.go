package main

import "fmt"

// Factorial 返回 n 的阶乘（n>=0）。
func Factorial(n int) int {
	result := 1
	for i := 2; i <= n; i++ {
		result *= i
	}
	return
}

func main() {
	fmt.Println(Factorial(5))
}
