package mathx

import "testing"

func TestSumTo(t *testing.T) {
	tests := []struct {
		n    int
		want int
	}{
		{0, 0},
		{1, 1},
		{3, 6},
		{5, 15},
		{10, 55},
	}
	for _, tt := range tests {
		if got := SumTo(tt.n); got != tt.want {
			t.Errorf("SumTo(%d) = %d, want %d", tt.n, got, tt.want)
		}
	}
}
