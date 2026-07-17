package perfx

import (
	"reflect"
	"testing"
	"time"
)

// perfBudget 是大输入去重的时间预算：线性实现远快于此，O(n^2) 实现则会超时。
const perfBudget = 2 * time.Second

// TestDedupCorrect 校验去重语义与首次出现顺序。
func TestDedupCorrect(t *testing.T) {
	in := []int{3, 1, 3, 2, 1, 2, 4}
	want := []int{3, 1, 2, 4}
	if got := Dedup(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("Dedup(%v) = %v, want %v", in, got, want)
	}
	if got := Dedup(nil); len(got) != 0 {
		t.Fatalf("Dedup(nil) = %v, want empty", got)
	}
}

// TestDedupPerformance 用大输入 + 时间预算判定复杂度：O(n^2) 实现会在预算内跑不完（判失败），
// 线性实现则毫秒级完成。用独立 goroutine + 超时避免慢实现拖垮整个测试时长。
// 输入全为唯一值（无重复），使 O(n^2) 实现的内层扫描无法提前 break，性能差距最大化、判定稳健。
func TestDedupPerformance(t *testing.T) {
	const n = 200000
	in := make([]int, n)
	for i := range in {
		in[i] = i // 全唯一，去重后仍为 n 个
	}
	done := make(chan int, 1)
	start := time.Now()
	go func() { done <- len(Dedup(in)) }()
	select {
	case got := <-done:
		if got != n {
			t.Fatalf("Dedup length = %d, want %d", got, n)
		}
		if d := time.Since(start); d > perfBudget {
			t.Fatalf("Dedup too slow: %v > %v (need linear time)", d, perfBudget)
		}
	case <-time.After(perfBudget):
		t.Fatalf("Dedup did not finish within %v (likely O(n^2))", perfBudget)
	}
}
