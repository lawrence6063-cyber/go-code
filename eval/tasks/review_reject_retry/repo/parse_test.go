package parsex

import "testing"

func TestParsePositive_Valid(t *testing.T) {
	got, err := ParsePositive("42")
	if err != nil || got != 42 {
		t.Fatalf("ParsePositive(%q) = %d,%v want 42,nil", "42", got, err)
	}
}

func TestParsePositive_Invalid(t *testing.T) {
	for _, s := range []string{"", "abc", "0", "-3"} {
		if _, err := ParsePositive(s); err == nil {
			t.Errorf("ParsePositive(%q) err = nil, want non-nil", s)
		}
	}
}
