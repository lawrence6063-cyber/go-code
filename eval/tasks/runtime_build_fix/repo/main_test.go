package main

import "testing"

// TestFactorial 校验 Factorial 的正确性。
func TestFactorial(t *testing.T) {
	cases := map[int]int{0: 1, 1: 1, 5: 120, 6: 720}
	for n, want := range cases {
		if got := Factorial(n); got != want {
			t.Errorf("Factorial(%d) = %d, want %d", n, got, want)
		}
	}
}
