package tool

import (
	"strings"
	"testing"
)

func TestUnifiedDiffIdentical(t *testing.T) {
	if got := unifiedDiff("x.go", "a\nb\n", "a\nb\n"); got != "" {
		t.Fatalf("identical input should yield empty diff, got %q", got)
	}
}

func TestUnifiedDiffHeader(t *testing.T) {
	got := unifiedDiff("pkg/x.go", "a\nb\nc\n", "a\nB\nc\n")
	if !strings.HasPrefix(got, "--- a/pkg/x.go\n+++ b/pkg/x.go\n") {
		t.Fatalf("missing/incorrect header: %q", got)
	}
}

func TestUnifiedDiffReplaceLine(t *testing.T) {
	got := unifiedDiff("x.go", "a\nold\nc\n", "a\nnew\nc\n")
	for _, s := range []string{"@@ -", "-old", "+new", " a", " c"} {
		if !strings.Contains(got, s) {
			t.Fatalf("diff missing %q in:\n%s", s, got)
		}
	}
}

func TestUnifiedDiffNewFile(t *testing.T) {
	got := unifiedDiff("new.go", "", "line1\nline2\n")
	if !strings.Contains(got, "+line1") || !strings.Contains(got, "+line2") {
		t.Fatalf("new-file diff should be all additions:\n%s", got)
	}
	if strings.Contains(got, "\n-") {
		t.Fatalf("new-file diff should have no deletions:\n%s", got)
	}
}

func TestUnifiedDiffContextBounded(t *testing.T) {
	var mid strings.Builder
	for i := 0; i < 40; i++ {
		mid.WriteString("line\n")
	}
	old := "HEAD\n" + mid.String() + "TAIL\n"
	neu := "CHANGED\n" + mid.String() + "CHANGED2\n"
	got := unifiedDiff("x.go", old, neu)
	if n := strings.Count(got, "@@ -"); n != 2 {
		t.Fatalf("expected 2 hunks, got %d:\n%s", n, got)
	}
}

func TestUnifiedDiffOversizeDegrades(t *testing.T) {
	big := strings.Repeat("x\n", maxDiffInputLines+1)
	if got := unifiedDiff("x.go", big, big+"y\n"); got != "" {
		t.Fatalf("oversize input should yield empty diff")
	}
}
