package guard

import "testing"

func TestClamp(t *testing.T) {
	cases := []struct {
		v, lo, hi, want int
	}{
		{-1, 0, 5, 0},
		{3, 0, 5, 3},
		{10, 0, 5, 5},
		{7, 2, 6, 6},
	}
	for _, c := range cases {
		if got := Clamp(c.v, c.lo, c.hi); got != c.want {
			t.Errorf("Clamp(%d,%d,%d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
		}
	}
}
