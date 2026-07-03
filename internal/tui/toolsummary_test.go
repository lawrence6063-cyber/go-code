package tui

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSummarize(t *testing.T) {
	cases := []struct {
		name  string
		tool  string
		input string
		want  string // 期望摘要包含的关键子串（去除 ANSI 后判断）
	}{
		{"read_file path", "read_file", `{"path":"src/x.go"}`, "read_file src/x.go"},
		{"write_file file_path", "write_file", `{"file_path":"a/b.go","content":"..."}`, "write_file a/b.go"},
		{"grep pattern", "grep", `{"pattern":"func\\s+\\w+"}`, "grep func"},
		{"bash command", "bash", `{"command":"go test ./..."}`, "bash go test ./..."},
		{"task description", "task", `{"description":"explore repo"}`, "task explore repo"},
		{"unknown fallback name", "mystery", `{"foo":1}`, "mystery"},
		{"invalid json falls back to name", "read_file", `{not json`, "read_file"},
		{"empty input returns name", "list_dir", ``, "list_dir"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Summarize(c.tool, json.RawMessage(c.input))
			if !strings.Contains(got, c.want) {
				t.Fatalf("Summarize(%q,%q)=%q, want contains %q", c.tool, c.input, got, c.want)
			}
		})
	}
}

func TestSummarizeTruncatesLongArg(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := Summarize("bash", json.RawMessage(`{"command":"`+long+`"}`))
	if !strings.HasSuffix(strings.TrimRight(got, " "), "…") {
		t.Fatalf("expected truncated summary ending with …, got %q", got)
	}
}
