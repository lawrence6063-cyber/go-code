package numkit

import (
	"errors"
	"testing"
)

func TestMax(t *testing.T) {
	if _, err := Max(nil); !errors.Is(err, ErrEmpty) {
		t.Fatalf("Max(nil) err = %v, want ErrEmpty", err)
	}
	got, err := Max([]int{3, 7, 2})
	if err != nil || got != 7 {
		t.Fatalf("Max([3,7,2]) = %d,%v want 7,nil", got, err)
	}
	got, err = Max([]int{-5, -1, -3})
	if err != nil || got != -1 {
		t.Fatalf("Max([-5,-1,-3]) = %d,%v want -1,nil", got, err)
	}
}

func TestMean(t *testing.T) {
	if _, err := Mean(nil); !errors.Is(err, ErrEmpty) {
		t.Fatalf("Mean(nil) err = %v, want ErrEmpty", err)
	}
	got, err := Mean([]int{1, 2, 3})
	if err != nil || got != 2 {
		t.Fatalf("Mean([1,2,3]) = %d,%v want 2,nil", got, err)
	}
	got, err = Mean([]int{-1, -2, -3})
	if err != nil || got != -2 {
		t.Fatalf("Mean([-1,-2,-3]) = %d,%v want -2,nil", got, err)
	}
}
