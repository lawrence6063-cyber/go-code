package main

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/alaindong/cogent/internal/permission"
)

// TestCLIPrompter_AlwaysAutoApprovesSameTool 验证大写 A 设置会话级 always 后，
// 同工具后续 Ask 直接批准且不消费 stdin（用不同工具能读到残留输入来证明）。
func TestCLIPrompter_AlwaysAutoApprovesSameTool(t *testing.T) {
	in := newInputReader(strings.NewReader("A\nreject-me\n"))
	p := newCLIPrompter(in).(*cliPrompter)
	ctx := context.Background()
	input := permission.Interrupt{Tool: "write_file", Input: []byte(`{"path":"a.go"}`)}

	// 第一次：读 "A" → 设 allowlist + 批准
	res1, err := p.Ask(ctx, input)
	if err != nil {
		t.Fatalf("first Ask: %v", err)
	}
	if res1.Action != permission.ActionApprove {
		t.Fatalf("first Ask action=%v want Approve", res1.Action)
	}
	p.mu.Lock()
	if !p.allow["write_file"] {
		t.Fatal("allow[write_file] not set after A")
	}
	p.mu.Unlock()

	// 第二次：同工具 → 短路批准，不读 stdin
	res2, err := p.Ask(ctx, input)
	if err != nil {
		t.Fatalf("second Ask: %v", err)
	}
	if res2.Action != permission.ActionApprove {
		t.Fatalf("second Ask action=%v want Approve (always)", res2.Action)
	}

	// 第三次：不同工具 edit_file → 应读到 "reject-me" 走 Reject（证明第二次没偷吃输入）
	res3, err := p.Ask(ctx, permission.Interrupt{Tool: "edit_file", Input: []byte(`{}`)})
	if err != nil {
		t.Fatalf("third Ask: %v", err)
	}
	if res3.Action != permission.ActionReject {
		t.Fatalf("third Ask action=%v want Reject (should read reject-me)", res3.Action)
	}
}

// TestCLIPrompter_AlwaysCaseSensitive 验证小写 a 不设 allowlist。
func TestCLIPrompter_AlwaysCaseSensitive(t *testing.T) {
	in := newInputReader(strings.NewReader("a\nr\n"))
	p := newCLIPrompter(in).(*cliPrompter)
	ctx := context.Background()
	input := permission.Interrupt{Tool: "write_file", Input: []byte(`{"path":"a.go"}`)}

	res1, _ := p.Ask(ctx, input)
	if res1.Action != permission.ActionApprove {
		t.Fatalf("first Ask action=%v want Approve", res1.Action)
	}
	p.mu.Lock()
	if p.allow["write_file"] {
		t.Fatal("lowercase a should NOT set allowlist")
	}
	p.mu.Unlock()
}

// TestCLIPrompter_AlwaysKeyword 验证 "always" 关键字等价于大写 A。
func TestCLIPrompter_AlwaysKeyword(t *testing.T) {
	in := newInputReader(strings.NewReader("always\n"))
	p := newCLIPrompter(in).(*cliPrompter)
	ctx := context.Background()
	input := permission.Interrupt{Tool: "bash", Input: []byte(`{"command":"ls"}`)}

	res, _ := p.Ask(ctx, input)
	if res.Action != permission.ActionApprove {
		t.Fatalf("action=%v want Approve", res.Action)
	}
	p.mu.Lock()
	if !p.allow["bash"] {
		t.Fatal("allow[bash] not set after 'always'")
	}
	p.mu.Unlock()
}

// TestCLIPrompter_AlwaysConcurrent 验证并发调用 Ask 无 race（-race 必跑）。
func TestCLIPrompter_AlwaysConcurrent(t *testing.T) {
	in := newInputReader(strings.NewReader(strings.Repeat("A\n", 20)))
	p := newCLIPrompter(in).(*cliPrompter)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.Ask(ctx, permission.Interrupt{Tool: "write_file", Input: []byte(`{}`)})
		}()
	}
	wg.Wait()
}
