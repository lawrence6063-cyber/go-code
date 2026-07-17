package terminalbench

import (
	"strings"
	"testing"
)

// TestParseBlockScalarInstruction 验证块标量 instruction 的收集与去缩进。
func TestParseBlockScalarInstruction(t *testing.T) {
	yaml := "instruction: |\n" +
		"  Create a file named greeting.txt in the current directory\n" +
		"  whose contents are exactly \"hello from cogent\".\n" +
		"difficulty: hard\n" +
		"tags: [file-manipulation, shell]\n"
	y := parseTaskYAML([]byte(yaml))
	if !strings.Contains(y.Instruction, "Create a file named greeting.txt") ||
		!strings.Contains(y.Instruction, "hello from cogent") {
		t.Fatalf("instruction block not parsed: %q", y.Instruction)
	}
	if strings.Contains(y.Instruction, "  ") {
		t.Errorf("instruction not dedented: %q", y.Instruction)
	}
	if y.Difficulty != "hard" {
		t.Errorf("difficulty = %q, want hard", y.Difficulty)
	}
	if len(y.Tags) != 2 || y.Tags[0] != "file-manipulation" || y.Tags[1] != "shell" {
		t.Errorf("inline tags wrong: %v", y.Tags)
	}
}

// TestParseInlineInstructionAndBlockListTags 验证内联 instruction 与块列表 tags。
func TestParseInlineInstructionAndBlockListTags(t *testing.T) {
	yaml := "instruction: Fix the failing build\n" +
		"tags:\n" +
		"  - build\n" +
		"  - compiler\n"
	y := parseTaskYAML([]byte(yaml))
	if y.Instruction != "Fix the failing build" {
		t.Errorf("inline instruction wrong: %q", y.Instruction)
	}
	if len(y.Tags) != 2 || y.Tags[0] != "build" || y.Tags[1] != "compiler" {
		t.Errorf("block-list tags wrong: %v", y.Tags)
	}
}

// TestParseDescriptionFallback 验证 description 键作为 instruction 的回退来源。
func TestParseDescriptionFallback(t *testing.T) {
	y := parseTaskYAML([]byte("description: do a thing\ndifficulty: medium\n"))
	if y.Instruction != "do a thing" || y.Difficulty != "medium" {
		t.Fatalf("description fallback wrong: %+v", y)
	}
}

// TestFilterHelpers 验证筛选辅助（AND 组合、大小写不敏感、tag 交集）。
func TestFilterHelpers(t *testing.T) {
	spec := TaskSpec{ID: "hello-world", YAML: TaskYAML{Difficulty: "hard", Tags: []string{"shell", "files"}}}
	cases := []struct {
		name string
		f    Filter
		want bool
	}{
		{"empty", Filter{}, true},
		{"id-hit", Filter{IDs: []string{"HELLO-WORLD"}}, true},
		{"id-miss", Filter{IDs: []string{"other"}}, false},
		{"tag-hit", Filter{Tags: []string{"files"}}, true},
		{"tag-miss", Filter{Tags: []string{"ml"}}, false},
		{"diff-hit", Filter{Difficulties: []string{"hard"}}, true},
		{"diff-miss", Filter{Difficulties: []string{"easy"}}, false},
		{"and", Filter{IDs: []string{"hello-world"}, Tags: []string{"ml"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matches(spec, tc.f); got != tc.want {
				t.Errorf("matches(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
