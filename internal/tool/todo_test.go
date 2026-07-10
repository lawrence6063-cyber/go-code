// Package tool 中的 todo_test.go 覆盖 todo_write 工具的整份覆盖语义与校验规则。
package tool

import (
	"context"
	"strings"
	"testing"

	"github.com/alaindong/cogent/internal/permission"
)

func TestTodoWrite_Metadata(t *testing.T) {
	tl := NewTodoWrite()
	if !tl.IsReadOnly(nil) {
		t.Error("todo_write should be read-only (no filesystem side effect)")
	}
	dec, _ := tl.CheckPermission(context.Background(), nil)
	if dec.Behavior != permission.BehaviorAllow {
		t.Errorf("CheckPermission behavior = %v, want allow", dec.Behavior)
	}
}

func TestTodoWrite_FullReplaceAndRender(t *testing.T) {
	tl := NewTodoWrite()

	in1 := map[string]any{"todos": []map[string]string{
		{"id": "1", "content": "add feature", "status": "in_progress"},
		{"id": "2", "content": "write tests", "status": "pending"},
	}}
	res, _ := tl.Call(context.Background(), mustJSON(t, in1), nil)
	if res.IsError {
		t.Fatalf("first write failed: %+v", res)
	}
	if !strings.Contains(res.Content, "[~] add feature (1)") || !strings.Contains(res.Content, "[ ] write tests (2)") {
		t.Errorf("render mismatch: %q", res.Content)
	}

	// 第二次调用整份覆盖：旧的 "2" 消失，新增 "3" 且状态变化。
	in2 := map[string]any{"todos": []map[string]string{
		{"id": "1", "content": "add feature", "status": "completed"},
		{"id": "3", "content": "ship it", "status": "pending"},
	}}
	res, _ = tl.Call(context.Background(), mustJSON(t, in2), nil)
	if res.IsError {
		t.Fatalf("second write failed: %+v", res)
	}
	if strings.Contains(res.Content, "write tests") {
		t.Errorf("full replace should drop previous items, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "[x] add feature (1)") || !strings.Contains(res.Content, "[ ] ship it (3)") {
		t.Errorf("render mismatch after replace: %q", res.Content)
	}

	tool := tl.(*todoTool)
	snap := tool.Snapshot()
	if len(snap) != 2 {
		t.Errorf("snapshot len = %d, want 2", len(snap))
	}
}

func TestTodoWrite_ValidationErrors(t *testing.T) {
	tests := []struct {
		name  string
		todos []map[string]string
	}{
		{"empty id", []map[string]string{{"id": "", "content": "x", "status": "pending"}}},
		{"empty content", []map[string]string{{"id": "1", "content": "", "status": "pending"}}},
		{"invalid status", []map[string]string{{"id": "1", "content": "x", "status": "done"}}},
		{"duplicate id", []map[string]string{
			{"id": "1", "content": "a", "status": "pending"},
			{"id": "1", "content": "b", "status": "pending"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tl := NewTodoWrite()
			res, _ := tl.Call(context.Background(), mustJSON(t, map[string]any{"todos": tt.todos}), nil)
			if !res.IsError {
				t.Errorf("expected validation error for %s, got %+v", tt.name, res)
			}
		})
	}
}

func TestTodoWrite_EmptyListRendersPlaceholder(t *testing.T) {
	tl := NewTodoWrite()
	res, _ := tl.Call(context.Background(), mustJSON(t, map[string]any{"todos": []map[string]string{}}), nil)
	if res.IsError || res.Content != "(todo list is empty)" {
		t.Errorf("empty list got %+v", res)
	}
}
