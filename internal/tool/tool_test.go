package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/types"
)

// fakeTool 是测试用工具替身，可配只读性，记录是否被调用及收到的入参。
type fakeTool struct {
	Defaults
	name     string
	readonly bool
	called   bool
	gotInput json.RawMessage
}

func (f *fakeTool) Name() string                    { return f.name }
func (f *fakeTool) Description() string             { return "fake tool" }
func (f *fakeTool) InputSchema() json.RawMessage    { return json.RawMessage(`{"type":"object"}`) }
func (f *fakeTool) IsReadOnly(json.RawMessage) bool { return f.readonly }

func (f *fakeTool) Call(_ context.Context, input json.RawMessage, _ ProgressSink) (types.ToolResult, error) {
	f.called = true
	f.gotInput = input
	return types.ToolResult{Content: "ok"}, nil
}

// fakePrompter 是测试用中断决策器，返回预置的处置结果。
type fakePrompter struct {
	res permission.Resolution
	err error
}

func (f fakePrompter) Ask(context.Context, permission.Interrupt) (permission.Resolution, error) {
	return f.res, f.err
}

func testTracer() observe.Tracer {
	prov, _ := observe.New(observe.Config{Enabled: false})
	return prov.Tracer()
}

func TestNewPool_DedupAndLookup(t *testing.T) {
	a := &fakeTool{name: "dup"}
	b := &fakeTool{name: "dup"} // 同名，应被忽略
	c := &fakeTool{name: "other"}
	p := NewPool(a, b, c, nil)

	if got := len(p.All()); got != 2 {
		t.Fatalf("pool size = %d, want 2", got)
	}
	got, ok := p.Get("dup")
	if !ok || got != a {
		t.Errorf("Get(dup) kept wrong instance (first should win)")
	}
	if _, ok := p.Get("missing"); ok {
		t.Error("Get(missing) = ok, want not found")
	}
	if n := len(p.Schemas()); n != 2 {
		t.Errorf("Schemas len = %d, want 2", n)
	}
}

func TestDefaults_FailClosed(t *testing.T) {
	var d Defaults
	if d.IsConcurrencySafe(nil) {
		t.Error("default IsConcurrencySafe = true, want false")
	}
	if d.IsReadOnly(nil) {
		t.Error("default IsReadOnly = true, want false")
	}
	dec, err := d.CheckPermission(context.Background(), nil)
	if err != nil {
		t.Fatalf("CheckPermission err: %v", err)
	}
	if dec.Behavior != permission.BehaviorAsk {
		t.Errorf("default behavior = %v, want ask", dec.Behavior)
	}
}

func TestGuard_PolicyAllowAndDeny(t *testing.T) {
	allowed := &fakeTool{name: "w"}
	gAllow := NewGuard(allowed, permission.StaticPolicy{Allow: map[string]bool{"w": true}}, nil, testTracer())
	res, err := gAllow.Call(context.Background(), json.RawMessage(`{"a":1}`), nil)
	if err != nil || res.IsError || !allowed.called {
		t.Fatalf("allow path failed: err=%v res=%+v called=%v", err, res, allowed.called)
	}

	denied := &fakeTool{name: "w"}
	gDeny := NewGuard(denied, permission.StaticPolicy{Deny: map[string]bool{"w": true}}, nil, testTracer())
	res, err = gDeny.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("deny path err: %v", err)
	}
	if !res.IsError || denied.called {
		t.Errorf("deny path: want IsError && not called, got res=%+v called=%v", res, denied.called)
	}
}

func TestGuard_AskApproveEditReject(t *testing.T) {
	// Approve：原样执行。
	tApprove := &fakeTool{name: "w"}
	gApprove := NewGuard(tApprove, nil, fakePrompter{res: permission.Resolution{Action: permission.ActionApprove}}, testTracer())
	if _, err := gApprove.Call(context.Background(), json.RawMessage(`{"x":1}`), nil); err != nil {
		t.Fatalf("approve err: %v", err)
	}
	if !tApprove.called {
		t.Error("approve: inner not called")
	}

	// Edit：用修正入参执行。
	tEdit := &fakeTool{name: "w"}
	edited := json.RawMessage(`{"x":2}`)
	gEdit := NewGuard(tEdit, nil, fakePrompter{res: permission.Resolution{Action: permission.ActionEdit, UpdatedInput: edited}}, testTracer())
	if _, err := gEdit.Call(context.Background(), json.RawMessage(`{"x":1}`), nil); err != nil {
		t.Fatalf("edit err: %v", err)
	}
	if string(tEdit.gotInput) != string(edited) {
		t.Errorf("edit: inner got %q, want %q", tEdit.gotInput, edited)
	}

	// Reject：不执行，回流带指引的错误结果。
	tReject := &fakeTool{name: "w"}
	gReject := NewGuard(tReject, nil, fakePrompter{res: permission.Resolution{Action: permission.ActionReject, Guidance: "use edit_file instead"}}, testTracer())
	res, err := gReject.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("reject err: %v", err)
	}
	if tReject.called {
		t.Error("reject: inner should not be called")
	}
	if !res.IsError || !strings.Contains(res.Content, "use edit_file instead") {
		t.Errorf("reject: want error result with guidance, got %+v", res)
	}
}

func TestGuard_AskWithoutPrompterDenies(t *testing.T) {
	tl := &fakeTool{name: "w"} // 默认 CheckPermission=ask
	g := NewGuard(tl, nil, nil, testTracer())
	res, err := g.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !res.IsError || tl.called {
		t.Errorf("no prompter: want fail-closed deny, got res=%+v called=%v", res, tl.called)
	}
}
