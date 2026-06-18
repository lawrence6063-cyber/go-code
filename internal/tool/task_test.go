package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/types"
)

// TestMain 在包级别断言无 goroutine 泄漏（spec §9.3）。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fakeSpawner 是 Spawner 的替身：返回预设摘要或错误，并记录收到的 prompt。
type fakeSpawner struct {
	summary   string
	err       error
	gotPrompt string
}

func (f *fakeSpawner) Spawn(_ context.Context, prompt string) (string, error) {
	f.gotPrompt = prompt
	return f.summary, f.err
}

func TestTaskTool_Metadata(t *testing.T) {
	tk := NewTask(&fakeSpawner{})
	if tk.Name() != "task" {
		t.Errorf("Name = %q, want task", tk.Name())
	}
	if !tk.IsReadOnly(nil) {
		t.Error("task tool should be read-only")
	}
	dec, err := tk.CheckPermission(context.Background(), nil)
	if err != nil {
		t.Fatalf("CheckPermission: %v", err)
	}
	if dec.Behavior != permission.BehaviorAllow {
		t.Errorf("permission = %v, want allow", dec.Behavior)
	}
	var schema map[string]any
	if err := json.Unmarshal(tk.InputSchema(), &schema); err != nil {
		t.Fatalf("InputSchema not valid JSON: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["prompt"]; !ok {
		t.Errorf("schema missing 'prompt' property: %v", schema)
	}
}

func TestTaskTool_Call(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		spawner     *fakeSpawner
		wantErr     bool
		wantContent string
		wantPrompt  string
	}{
		{
			name:        "summary returned",
			input:       `{"prompt":"find the auth middleware"}`,
			spawner:     &fakeSpawner{summary: "located in internal/auth/mw.go"},
			wantContent: "located in internal/auth/mw.go",
			wantPrompt:  "find the auth middleware",
		},
		{
			name:        "spawn error normalized to IsError",
			input:       `{"prompt":"explore"}`,
			spawner:     &fakeSpawner{err: errors.New("llm down")},
			wantErr:     true,
			wantContent: "sub-agent failed",
		},
		{
			name:        "empty prompt rejected",
			input:       `{"prompt":"  "}`,
			spawner:     &fakeSpawner{},
			wantErr:     true,
			wantContent: "empty prompt",
		},
		{
			name:        "invalid json rejected",
			input:       `{not json}`,
			spawner:     &fakeSpawner{},
			wantErr:     true,
			wantContent: "invalid input",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tk := NewTask(tt.spawner)
			res, err := tk.Call(context.Background(), json.RawMessage(tt.input), nil)
			if err != nil {
				t.Fatalf("Call returned error (should normalize to IsError): %v", err)
			}
			if res.IsError != tt.wantErr {
				t.Errorf("IsError = %v, want %v (content=%q)", res.IsError, tt.wantErr, res.Content)
			}
			if !strings.Contains(res.Content, tt.wantContent) {
				t.Errorf("content = %q, want contains %q", res.Content, tt.wantContent)
			}
			if tt.wantPrompt != "" && tt.spawner.gotPrompt != tt.wantPrompt {
				t.Errorf("spawner got prompt %q, want %q", tt.spawner.gotPrompt, tt.wantPrompt)
			}
		})
	}
}

// TestTaskTool_CallNeverReturnsError 锁定工具错误规范化惯例：派发失败也不向上抛 error。
func TestTaskTool_CallNeverReturnsError(t *testing.T) {
	tk := NewTask(&fakeSpawner{err: errors.New("boom")})
	res, err := tk.Call(context.Background(), json.RawMessage(`{"prompt":"x"}`), nil)
	if err != nil {
		t.Fatalf("unexpected error return: %v", err)
	}
	if !res.IsError {
		t.Error("expected IsError result")
	}
	_ = types.ToolResult{} // 锚定类型引用
}
