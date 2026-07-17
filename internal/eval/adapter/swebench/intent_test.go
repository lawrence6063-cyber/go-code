package swebench

import (
	"strings"
	"testing"
)

// TestIntentScaffoldToggle 验证 intent() 在 scaffold 开/关两种模式下的内容差异，
// 且两种模式都注入 issue 文本、绝不泄露隐藏判定测试（FAIL_TO_PASS ids / test_patch）。
func TestIntentScaffoldToggle(t *testing.T) {
	inst := Instance{
		InstanceID:       "acme__widget-42",
		Repo:             "acme/widget",
		BaseCommit:       "deadbeef",
		ProblemStatement: "Widget.foo() returns None instead of 0 when input is empty.",
		TestPatch:        "diff --git a/tests/test_widget.py b/tests/test_widget.py",
		FailToPass:       []string{"tests/test_widget.py::test_empty_returns_zero"},
	}
	var a Adapter

	t.Run("scaffold_on_default", func(t *testing.T) {
		t.Setenv(scaffoldEnvVar, "")
		got := a.intent(inst)
		for _, want := range []string{
			"acme/widget", "deadbeef", inst.ProblemStatement,
			"LOCATE FIRST", "MINIMAL FIX", "PATCH HYGIENE", "hidden test suite",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("scaffold intent missing %q", want)
			}
		}
		assertNoLeak(t, got, inst)
	})

	t.Run("scaffold_off", func(t *testing.T) {
		t.Setenv(scaffoldEnvVar, "0")
		got := a.intent(inst)
		if !strings.Contains(got, "Modify only non-test source files") {
			t.Errorf("legacy intent missing minimal guidance")
		}
		if strings.Contains(got, "LOCATE FIRST") {
			t.Errorf("legacy intent must not contain scaffold guidance")
		}
		if !strings.Contains(got, inst.ProblemStatement) {
			t.Errorf("legacy intent missing problem statement")
		}
		assertNoLeak(t, got, inst)
	})
}

// TestScaffoldEnabled 验证开关解析：仅显式关闭值回退，其余（含未设置/空/杂值）均启用。
func TestScaffoldEnabled(t *testing.T) {
	off := map[string]bool{"0": true, "false": true, "OFF": true, "No": true}
	on := []string{"", "1", "true", "on", "yes", "whatever"}
	for v := range off {
		t.Setenv(scaffoldEnvVar, v)
		if scaffoldEnabled() {
			t.Errorf("%q should disable scaffold", v)
		}
	}
	for _, v := range on {
		t.Setenv(scaffoldEnvVar, v)
		if !scaffoldEnabled() {
			t.Errorf("%q should enable scaffold", v)
		}
	}
}

// assertNoLeak 确认意图不泄露隐藏判定测试标识与 test_patch 内容（防面向测试作弊）。
func assertNoLeak(t *testing.T, intent string, inst Instance) {
	t.Helper()
	if strings.Contains(intent, inst.TestPatch) {
		t.Errorf("intent leaked test_patch content")
	}
	for _, id := range inst.FailToPass {
		if strings.Contains(intent, id) {
			t.Errorf("intent leaked FAIL_TO_PASS id %q", id)
		}
	}
}
