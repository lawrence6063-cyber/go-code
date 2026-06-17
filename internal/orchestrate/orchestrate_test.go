package orchestrate

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/alaindong/cogent/internal/observe"
	"github.com/alaindong/cogent/internal/permission"
	"github.com/alaindong/cogent/internal/tool"
	"github.com/alaindong/cogent/internal/types"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fakeTool 是 tool.Tool 的测试替身，可配置并发安全性。
type fakeTool struct {
	tool.Defaults
	name       string
	concurrent bool
}

func (f *fakeTool) Name() string                           { return f.name }
func (f *fakeTool) Description() string                    { return "fake" }
func (f *fakeTool) InputSchema() json.RawMessage           { return json.RawMessage(`{"type":"object"}`) }
func (f *fakeTool) IsConcurrencySafe(json.RawMessage) bool { return f.concurrent }

func (f *fakeTool) CheckPermission(context.Context, json.RawMessage) (permission.Decision, error) {
	return permission.Decision{Behavior: permission.BehaviorAllow}, nil
}

func (f *fakeTool) Call(context.Context, json.RawMessage, tool.ProgressSink) (types.ToolResult, error) {
	return types.ToolResult{}, nil
}

func testTracer() observe.Tracer {
	prov, _ := observe.New(observe.Config{Enabled: false})
	return prov.Tracer()
}

func blocks(names ...string) []types.ToolUseBlock {
	out := make([]types.ToolUseBlock, len(names))
	for i, n := range names {
		out[i] = types.ToolUseBlock{ID: n, Name: n}
	}
	return out
}

// shape 把分批结果压缩成 "c2,s1" 形式（c=并发批+批大小，s=串行批+批大小），便于断言。
func shape(batches []Batch) []string {
	out := make([]string, len(batches))
	for i, b := range batches {
		kind := "s"
		if b.Concurrent {
			kind = "c"
		}
		out[i] = kind + itoa(len(b.Blocks))
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}

func TestPartitionBatches(t *testing.T) {
	pool := tool.NewPool(
		&fakeTool{name: "read", concurrent: true},
		&fakeTool{name: "grep", concurrent: true},
		&fakeTool{name: "write", concurrent: false},
		&fakeTool{name: "bash", concurrent: false},
	)
	tests := []struct {
		name   string
		blocks []types.ToolUseBlock
		pool   tool.Pool
		want   []string
	}{
		{"empty", nil, pool, []string{}},
		{"all concurrent merge", blocks("read", "grep", "read"), pool, []string{"c3"}},
		{"all serial", blocks("write", "bash"), pool, []string{"s1", "s1"}},
		{"mixed flush", blocks("read", "grep", "write", "read"), pool, []string{"c2", "s1", "c1"}},
		{"serial then concurrent", blocks("write", "read", "grep"), pool, []string{"s1", "c2"}},
		{"unknown tool serial", blocks("read", "ghost", "grep"), pool, []string{"c1", "s1", "c1"}},
		{"nil pool all serial", blocks("read", "grep"), nil, []string{"s1", "s1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shape(PartitionBatches(tt.blocks, tt.pool))
			if !equalStrs(got, tt.want) {
				t.Errorf("shape = %v, want %v", got, tt.want)
			}
		})
	}
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRun_PreservesRequestOrder(t *testing.T) {
	bs := blocks("a", "b", "c", "d")
	batches := []Batch{{Concurrent: true, Blocks: bs}}
	run := func(_ context.Context, block types.ToolUseBlock) types.Message {
		return types.Message{Role: types.RoleTool, ToolUseID: block.ID, ToolName: block.Name}
	}
	got := Run(context.Background(), batches, run, testTracer())
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	for i, want := range []string{"a", "b", "c", "d"} {
		if got[i].ToolUseID != want {
			t.Errorf("result[%d].ID = %q, want %q (order not preserved)", i, got[i].ToolUseID, want)
		}
	}
}

func TestRun_ConcurrentActuallyParallel(t *testing.T) {
	var inFlight, maxInFlight int32
	run := func(_ context.Context, block types.ToolUseBlock) types.Message {
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&maxInFlight)
			if cur <= old || atomic.CompareAndSwapInt32(&maxInFlight, old, cur) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		return types.Message{Role: types.RoleTool, ToolUseID: block.ID}
	}
	batches := []Batch{{Concurrent: true, Blocks: blocks("a", "b", "c")}}
	Run(context.Background(), batches, run, testTracer())
	if atomic.LoadInt32(&maxInFlight) < 2 {
		t.Errorf("maxInFlight = %d, want >= 2 (not parallel)", maxInFlight)
	}
}

func TestRun_CancelSkipsRemaining(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消
	var calls int32
	run := func(_ context.Context, block types.ToolUseBlock) types.Message {
		atomic.AddInt32(&calls, 1)
		return types.Message{Role: types.RoleTool, ToolUseID: block.ID}
	}
	batches := []Batch{
		{Concurrent: false, Blocks: blocks("a")},
		{Concurrent: false, Blocks: blocks("b")},
	}
	got := Run(ctx, batches, run, testTracer())
	// 取消时跳过未执行块且不产出零值 Message。
	if len(got) != 0 {
		t.Errorf("results = %d, want 0 (should skip on cancel)", len(got))
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Errorf("calls = %d, want 0", calls)
	}
}
