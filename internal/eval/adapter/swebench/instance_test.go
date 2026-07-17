package swebench

import (
	"strings"
	"testing"
)

// TestParseInstancesStringEncodedLists 验证 FAIL_TO_PASS/PASS_TO_PASS 兼容「JSON 字符串套数组」
// （SWE-bench HuggingFace 导出常用写法）与直接数组两种形式。
func TestParseInstancesStringEncodedLists(t *testing.T) {
	jsonl := `{"instance_id":"a__b-1","repo":"a/b","base_commit":"deadbeef","problem_statement":"fix it","test_patch":"diff","FAIL_TO_PASS":"[\"t1\", \"t2\"]","PASS_TO_PASS":["t3"]}
{"instance_id":"a__b-2","repo":"a/b","base_commit":"cafe","FAIL_TO_PASS":[]}`
	insts, err := parseInstances(strings.NewReader(jsonl), Filter{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(insts) != 2 {
		t.Fatalf("want 2 instances, got %d", len(insts))
	}
	if got := insts[0].FailToPass; len(got) != 2 || got[0] != "t1" || got[1] != "t2" {
		t.Errorf("FAIL_TO_PASS string-encoded parse wrong: %v", got)
	}
	if got := insts[0].PassToPass; len(got) != 1 || got[0] != "t3" {
		t.Errorf("PASS_TO_PASS array parse wrong: %v", got)
	}
	if len(insts[1].FailToPass) != 0 {
		t.Errorf("empty FAIL_TO_PASS should parse to empty, got %v", insts[1].FailToPass)
	}
}

// TestParseInstancesSkipsBlankAndComment 验证空行与注释行被跳过。
func TestParseInstancesSkipsBlankAndComment(t *testing.T) {
	jsonl := "\n# a comment line\n{\"instance_id\":\"x-1\",\"repo\":\"o/x\"}\n\n"
	insts, err := parseInstances(strings.NewReader(jsonl), Filter{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(insts) != 1 || insts[0].InstanceID != "x-1" {
		t.Fatalf("want single instance x-1, got %+v", insts)
	}
}

// TestParseInstancesFilter 验证 instance_id / repo / limit 三类筛选（AND 组合、大小写不敏感）。
func TestParseInstancesFilter(t *testing.T) {
	jsonl := `{"instance_id":"a__b-1","repo":"a/b"}
{"instance_id":"a__b-2","repo":"a/b"}
{"instance_id":"c__d-9","repo":"c/d"}`
	cases := []struct {
		name string
		f    Filter
		want []string
	}{
		{"id", Filter{InstanceIDs: []string{"A__B-2"}}, []string{"a__b-2"}},
		{"repo", Filter{Repos: []string{"c/d"}}, []string{"c__d-9"}},
		{"limit", Filter{Limit: 2}, []string{"a__b-1", "a__b-2"}},
		{"none", Filter{}, []string{"a__b-1", "a__b-2", "c__d-9"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			insts, err := parseInstances(strings.NewReader(jsonl), tc.f)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			var got []string
			for _, i := range insts {
				got = append(got, i.InstanceID)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("filter %s: want %v got %v", tc.name, tc.want, got)
			}
		})
	}
}

// TestParseInstancesBadLine 验证损坏行 fail-fast 报错（不静默吞掉）。
func TestParseInstancesBadLine(t *testing.T) {
	if _, err := parseInstances(strings.NewReader("{not json}"), Filter{}); err == nil {
		t.Fatal("expected error on malformed JSONL line")
	}
}

// TestPatchPaths 验证从统一 diff 抽取被触及文件路径（取 b/ 路径、去重）。
func TestPatchPaths(t *testing.T) {
	patch := "diff --git a/foo/bar.py b/foo/bar.py\n--- a/foo/bar.py\n+++ b/foo/bar.py\n" +
		"diff --git a/new_test.py b/new_test.py\nnew file mode 100644\n"
	got := patchPaths(patch)
	if len(got) != 2 || got[0] != "foo/bar.py" || got[1] != "new_test.py" {
		t.Fatalf("patchPaths wrong: %v", got)
	}
}

// TestTestCommandDerivation 验证判定命令推导：显式 TestCmd 优先；否则按语言/默认 pytest。
func TestTestCommandDerivation(t *testing.T) {
	if got := testCommand(Instance{TestCmd: "make test"}, t.TempDir()); got != "make test" {
		t.Errorf("explicit TestCmd should win, got %q", got)
	}
	inst := Instance{FailToPass: []string{"tests/test_a.py::test_x"}, PassToPass: []string{"tests/test_b.py::test_y"}}
	got := testCommand(inst, t.TempDir())
	if !strings.HasPrefix(got, "python -m pytest -q ") ||
		!strings.Contains(got, "test_a.py::test_x") || !strings.Contains(got, "test_b.py::test_y") {
		t.Errorf("pytest command derivation wrong: %q", got)
	}
}
