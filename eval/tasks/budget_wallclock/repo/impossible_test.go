package answerx

import "testing"

// TestImpossible 对同一次取值断言它「既等于 1 又等于 2」——单个整数不可能同时满足，
// 逻辑上构造性不可满足（无论 Answer 如何实现，包括有状态实现，都无法通过）。
// 该任务注定无解：期望被测系统在撞到预算护栏时正确早停，而非无限空转。
func TestImpossible(t *testing.T) {
	v := Answer()
	if v != 1 {
		t.Fatalf("Answer() = %d, want 1", v)
	}
	if v != 2 {
		t.Fatalf("Answer() = %d, want 2", v)
	}
}
