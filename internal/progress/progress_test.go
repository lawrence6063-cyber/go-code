package progress

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/goleak"
)

// TestMain 在包级别断言无 goroutine 泄漏（spec §9.3）。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestBoard_LoadMissingReturnsEmpty(t *testing.T) {
	b := NewBoard()
	items, err := b.Load(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("items = %d, want 0 for missing board", len(items))
	}
}

func TestBoard_UpsertRoundTrip(t *testing.T) {
	root := t.TempDir()
	b := NewBoard()

	want := Item{ID: "fix-x", Title: "Fix parser bug", Status: StatusDone, Note: "achieved in 2", Updated: 1700000000}
	if err := b.Upsert(context.Background(), root, want); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := b.Load(context.Background(), root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("items = %d, want 1", len(got))
	}
	if got[0] != want {
		t.Errorf("round-trip = %+v, want %+v", got[0], want)
	}
	// 看板文件应落在控制面 .cogent/ 下。
	if _, err := os.Stat(filepath.Join(root, BoardDir, BoardFile)); err != nil {
		t.Errorf("progress file not created: %v", err)
	}
}

func TestBoard_UpsertUpdatesByID(t *testing.T) {
	root := t.TempDir()
	b := NewBoard()
	ctx := context.Background()

	_ = b.Upsert(ctx, root, Item{ID: "a", Title: "task a", Status: StatusTodo, Updated: 1})
	_ = b.Upsert(ctx, root, Item{ID: "b", Title: "task b", Status: StatusDoing, Updated: 2})
	_ = b.Upsert(ctx, root, Item{ID: "a", Title: "task a", Status: StatusDone, Note: "fixed", Updated: 3})

	got, err := b.Load(ctx, root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("items = %d, want 2 (a updated in place, b kept)", len(got))
	}
	byID := map[string]Item{}
	for _, it := range got {
		byID[it.ID] = it
	}
	if byID["a"].Status != StatusDone || byID["a"].Note != "fixed" {
		t.Errorf("item a = %+v, want updated to done/fixed", byID["a"])
	}
	if byID["b"].Status != StatusDoing {
		t.Errorf("item b = %+v, want preserved doing", byID["b"])
	}
}

func TestBoard_UpsertSetsUpdatedWhenZero(t *testing.T) {
	root := t.TempDir()
	b := NewBoard()
	if err := b.Upsert(context.Background(), root, Item{ID: "x", Title: "t", Status: StatusTodo}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, _ := b.Load(context.Background(), root)
	if len(got) != 1 || got[0].Updated == 0 {
		t.Errorf("Updated should be auto-filled, got %+v", got)
	}
}

func TestBoard_UpsertRejectsEmptyID(t *testing.T) {
	if err := NewBoard().Upsert(context.Background(), t.TempDir(), Item{ID: "  "}); err == nil {
		t.Error("expected error for empty id, got nil")
	}
}

func TestBoard_LoadTolerantToBadRows(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, BoardDir)
	if err := os.MkdirAll(dir, dirPerms); err != nil {
		t.Fatal(err)
	}
	content := `# cogent progress

| ID | Status | Title | Note | Updated |
| --- | --- | --- | --- | --- |
| good-1 | done | ok title | note | 100 |
this is a junk line, not a row
| broken-too-few | done |
| good-2 | blocked | another | n2 | 200 |
`
	if err := os.WriteFile(filepath.Join(dir, BoardFile), []byte(content), filePerms); err != nil {
		t.Fatal(err)
	}
	got, err := NewBoard().Load(context.Background(), root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("items = %d, want 2 (bad rows skipped): %+v", len(got), got)
	}
	if got[0].ID != "good-1" || got[1].ID != "good-2" || got[1].Status != StatusBlocked {
		t.Errorf("parsed items unexpected: %+v", got)
	}
}

func TestParseStatus(t *testing.T) {
	tests := []struct {
		in   string
		want Status
	}{
		{"todo", StatusTodo}, {"doing", StatusDoing}, {"done", StatusDone},
		{"blocked", StatusBlocked}, {"DONE", StatusDone}, {"garbage", StatusTodo},
	}
	for _, tt := range tests {
		if got := ParseStatus(tt.in); got != tt.want {
			t.Errorf("ParseStatus(%q) = %v, want %v", tt.in, got, tt.want)
		}
		// String/ParseStatus 往返一致。
		if got := ParseStatus(tt.want.String()); got != tt.want {
			t.Errorf("round-trip %v failed: got %v", tt.want, got)
		}
	}
}
