package render

import (
	"strings"
	"testing"
)

func TestDisplayWidth(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"ascii", "hello", 5},
		{"empty", "", 0},
		{"cjk", "你好", 4},
		{"mixed", "a你b", 4},
		{"fullwidth", "ＡＢ", 4},
		{"control", "a\tb", 2},
		{"combining", "e\u0301", 1}, // e + 组合尖音符
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DisplayWidth(c.in); got != c.want {
				t.Fatalf("DisplayWidth(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		name string
		in   string
		cols int
		want string
	}{
		{"no-truncate", "hello", 10, "hello"},
		{"exact", "hello", 5, "hello"},
		{"ascii-cut", "hello world", 5, "hell…"},
		{"zero", "hello", 0, ""},
		{"cjk-cut", "你好世界", 5, "你好…"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Truncate(c.in, c.cols); got != c.want {
				t.Fatalf("Truncate(%q, %d) = %q, want %q", c.in, c.cols, got, c.want)
			}
		})
	}
}

func TestTruncateWidthBound(t *testing.T) {
	// 截断结果的显示宽度不得超过 cols。
	for cols := 1; cols <= 8; cols++ {
		got := Truncate("你a好b世c界", cols)
		if w := DisplayWidth(got); w > cols {
			t.Fatalf("Truncate cols=%d produced width %d (%q)", cols, w, got)
		}
	}
}

func TestRenderMarkdownHeading(t *testing.T) {
	got := RenderMarkdown("# Title")
	if !strings.HasPrefix(got, ansiBold) || !strings.HasSuffix(got, ansiReset) {
		t.Fatalf("heading not bolded: %q", got)
	}
}

func TestRenderMarkdownInline(t *testing.T) {
	got := RenderMarkdown("a **b** c `d` e")
	if !strings.Contains(got, ansiBold+"b"+ansiReset) {
		t.Fatalf("bold not applied: %q", got)
	}
	if !strings.Contains(got, ansiFgCyan+"d"+ansiReset) {
		t.Fatalf("inline code not applied: %q", got)
	}
	// 未着色的普通字符应保留。
	if !strings.Contains(got, "a ") || !strings.Contains(got, " e") {
		t.Fatalf("plain text lost: %q", got)
	}
}

func TestRenderMarkdownUnclosedMarker(t *testing.T) {
	// 未闭合标记应原样保留，不吞字符。
	got := RenderMarkdown("a **b c")
	if !strings.Contains(got, "**b c") {
		t.Fatalf("unclosed marker mangled: %q", got)
	}
}

func TestRenderMarkdownFence(t *testing.T) {
	src := "text\n```go\ncode\n```\nmore"
	got := RenderMarkdown(src)
	if !strings.Contains(got, ansiFgCyan+"code"+ansiReset) {
		t.Fatalf("fenced code not colored: %q", got)
	}
	// 行数保持不变（保留换行）。
	if strings.Count(got, "\n") != strings.Count(src, "\n") {
		t.Fatalf("line count changed: %q", got)
	}
}

func TestColorizeDiff(t *testing.T) {
	unified := strings.Join([]string{
		"--- a/x.go",
		"+++ b/x.go",
		"@@ -1,2 +1,2 @@",
		" ctx",
		"-old",
		"+new",
	}, "\n")
	got := ColorizeDiff(unified)
	if !strings.Contains(got, ansiFgGreen+"+new"+ansiReset) {
		t.Fatalf("added line not green: %q", got)
	}
	if !strings.Contains(got, ansiFgRed+"-old"+ansiReset) {
		t.Fatalf("removed line not red: %q", got)
	}
	if !strings.Contains(got, ansiFgCyan+"@@ -1,2 +1,2 @@"+ansiReset) {
		t.Fatalf("hunk header not cyan: %q", got)
	}
	// +++/--- 属于文件头，必须加粗而非红/绿。
	if !strings.Contains(got, ansiBold+"+++ b/x.go"+ansiReset) {
		t.Fatalf("file header not bold: %q", got)
	}
}

func TestColorizeDiffEmpty(t *testing.T) {
	if got := ColorizeDiff(""); got != "" {
		t.Fatalf("empty diff should return empty, got %q", got)
	}
}

func TestColorizeDiffOversizeDegrades(t *testing.T) {
	big := strings.Repeat("+line\n", maxDiffLines+1)
	if got := ColorizeDiff(big); got != big {
		t.Fatalf("oversize diff should degrade to plain text")
	}
}
