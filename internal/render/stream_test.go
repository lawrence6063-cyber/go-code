package render

import (
	"strings"
	"testing"
)

func TestStreamMarkdownBuffersPartialLine(t *testing.T) {
	var s StreamMarkdown
	// 无换行的块应被缓冲，不产生输出。
	if got := s.Write("partial "); got != "" {
		t.Fatalf("partial chunk should buffer, got %q", got)
	}
	out := s.Write("line\n")
	if !strings.Contains(out, "partial line") {
		t.Fatalf("completed line not emitted: %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("newline not preserved: %q", out)
	}
}

func TestStreamMarkdownFlush(t *testing.T) {
	var s StreamMarkdown
	s.Write("no newline yet")
	out := s.Flush()
	if !strings.Contains(out, "no newline yet") {
		t.Fatalf("flush lost buffered text: %q", out)
	}
	if s.Flush() != "" {
		t.Fatalf("second flush should be empty")
	}
}

func TestStreamMarkdownFenceAcrossChunks(t *testing.T) {
	var s StreamMarkdown
	var b strings.Builder
	b.WriteString(s.Write("```go\n"))
	b.WriteString(s.Write("code line\n"))
	b.WriteString(s.Write("```\n"))
	got := b.String()
	if !strings.Contains(got, ansiFgCyan+"code line"+ansiReset) {
		t.Fatalf("fenced line across chunks not colored: %q", got)
	}
}

func TestStreamMarkdownHeadingAndInline(t *testing.T) {
	var s StreamMarkdown
	got := s.Write("# Title\nplain **bold** end\n")
	if !strings.Contains(got, ansiBold+"# Title"+ansiReset) {
		t.Fatalf("heading not bolded: %q", got)
	}
	if !strings.Contains(got, ansiBold+"bold"+ansiReset) {
		t.Fatalf("inline bold not applied: %q", got)
	}
}
