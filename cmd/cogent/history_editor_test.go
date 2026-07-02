package main

import (
	"context"
	"testing"

	"github.com/alaindong/cogent/internal/history"
)

// feedRunes 依次把字符串的每个 rune 作为按键喂给 editorCore。
func feedRunes(e *editorCore, s string) {
	for _, r := range s {
		e.handleKey(runeEvent(r))
	}
}

func newHistoryCore(t *testing.T, entries ...string) *editorCore {
	t.Helper()
	e := newEditorCore(context.Background(), stubProvider{})
	h := history.NewMemory()
	for _, entry := range entries {
		h.Append(entry)
	}
	e.history = h
	return e
}

func TestHistoryPrevNext(t *testing.T) {
	e := newHistoryCore(t, "first", "second")
	// 空行 ↑ 应载入最近一条。
	e.handleKey(ev(keyUp))
	if string(e.line) != "second" {
		t.Fatalf("Up1 line = %q, want second", string(e.line))
	}
	e.handleKey(ev(keyUp))
	if string(e.line) != "first" {
		t.Fatalf("Up2 line = %q, want first", string(e.line))
	}
	// ↓ 回到更近一条。
	e.handleKey(ev(keyDown))
	if string(e.line) != "second" {
		t.Fatalf("Down line = %q, want second", string(e.line))
	}
}

func TestHistoryPrevRestoresStash(t *testing.T) {
	e := newHistoryCore(t, "old")
	feedRunes(e, "draft") // 用户正在编辑
	e.handleKey(ev(keyUp))
	if string(e.line) != "old" {
		t.Fatalf("Up line = %q, want old", string(e.line))
	}
	e.handleKey(ev(keyDown)) // 越过最近，回到暂存草稿
	if string(e.line) != "draft" {
		t.Fatalf("restored line = %q, want draft", string(e.line))
	}
}

func TestReverseSearchBasic(t *testing.T) {
	e := newHistoryCore(t, "go build", "git status", "go test")
	e.handleKey(ev(keyCtrlR)) // 进入搜索
	if e.mode != modeSearch {
		t.Fatalf("Ctrl-R should enter modeSearch")
	}
	feedRunes(e, "go") // 增量搜索 "go"
	if string(e.line) != "go test" {
		t.Fatalf("search line = %q, want go test", string(e.line))
	}
	e.handleKey(ev(keyCtrlR)) // 跳更早的匹配
	if string(e.line) != "go build" {
		t.Fatalf("next match = %q, want go build", string(e.line))
	}
	e.handleKey(ev(keyEnter)) // 采纳并退出搜索
	if e.mode != modeNormal {
		t.Fatalf("Enter should leave modeSearch")
	}
	if string(e.line) != "go build" {
		t.Fatalf("accepted line = %q, want go build", string(e.line))
	}
}

func TestReverseSearchCancelRestores(t *testing.T) {
	e := newHistoryCore(t, "committed")
	feedRunes(e, "wip")
	e.handleKey(ev(keyCtrlR))
	feedRunes(e, "comm") // 预览匹配到 committed
	if string(e.line) != "committed" {
		t.Fatalf("preview = %q, want committed", string(e.line))
	}
	e.handleKey(ev(keyCtrlG)) // 取消，恢复原编辑
	if e.mode != modeNormal || string(e.line) != "wip" {
		t.Fatalf("cancel: mode=%v line=%q, want normal/wip", e.mode, string(e.line))
	}
}

func TestDropdownTakesPriorityOverHistory(t *testing.T) {
	// 下拉激活时 ↑ 应移动候选而非翻历史。
	e := newHistoryCore(t, "hist entry")
	e.provider = stubProvider{list: []string{"a.go", "b.go", "c.go"}}
	feedRunes(e, "@") // 触发文件补全下拉
	if !e.active {
		t.Fatalf("@ should activate dropdown")
	}
	before := string(e.line)
	e.handleKey(ev(keyUp)) // 应移动候选，不改行内容
	if string(e.line) != before {
		t.Fatalf("Up with active dropdown must not load history; line=%q", string(e.line))
	}
	if e.sel != len(e.sugg)-1 {
		t.Fatalf("Up should wrap selection to last, sel=%d", e.sel)
	}
}
