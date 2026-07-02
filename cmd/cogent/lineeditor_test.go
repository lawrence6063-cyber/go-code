package main

import (
	"context"
	"strings"
	"testing"
)

// stubProvider 是测试用候选来源：返回以 partial 过滤后的固定清单。
type stubProvider struct {
	all []string
}

func (s stubProvider) Filter(_ context.Context, partial string, limit int) []string {
	var out []string
	for _, p := range s.all {
		if partial == "" || strings.Contains(p, partial) {
			out = append(out, p)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func newTestCore(all ...string) *editorCore {
	return newEditorCore(context.Background(), stubProvider{all: all})
}

// typeRunes 依次输入若干普通字符。
func typeRunes(e *editorCore, s string) {
	for _, r := range s {
		e.handleKey(runeEvent(r))
	}
}

// TestEditor_PlainTyping 验证普通输入与提交。
func TestEditor_PlainTyping(t *testing.T) {
	e := newTestCore()
	typeRunes(e, "hello")
	if got := e.handleKey(ev(keyEnter)); got != statusSubmit {
		t.Fatalf("enter status=%v want submit", got)
	}
	if string(e.line) != "hello" {
		t.Fatalf("line=%q want hello", string(e.line))
	}
}

// TestEditor_AtTriggersDropdown 验证输入 @ 后即触发下拉并随片段过滤。
func TestEditor_AtTriggersDropdown(t *testing.T) {
	e := newTestCore("internal/tool/grep.go", "main.go", "internal/tool/readfile.go")
	typeRunes(e, "see @")
	if !e.active || len(e.sugg) != 3 {
		t.Fatalf("after @: active=%v n=%d want active,3", e.active, len(e.sugg))
	}
	typeRunes(e, "grep")
	if !e.active || len(e.sugg) != 1 || e.sugg[0] != "internal/tool/grep.go" {
		t.Fatalf("after grep: sugg=%v want [internal/tool/grep.go]", e.sugg)
	}
}

// TestEditor_SpaceClosesDropdown 验证片段后遇空白关闭下拉。
func TestEditor_SpaceClosesDropdown(t *testing.T) {
	e := newTestCore("main.go")
	typeRunes(e, "@main")
	if !e.active {
		t.Fatal("dropdown should be active")
	}
	e.handleKey(runeEvent(' '))
	if e.active {
		t.Fatal("space should close dropdown")
	}
}

// TestEditor_AcceptWritesBack 验证 Enter 在下拉激活时选中写回并保留 @ 前缀、不提交。
func TestEditor_AcceptWritesBack(t *testing.T) {
	e := newTestCore("internal/tool/grep.go")
	typeRunes(e, "@grep")
	if got := e.handleKey(ev(keyEnter)); got != statusContinue {
		t.Fatalf("enter with dropdown status=%v want continue", got)
	}
	if string(e.line) != "@internal/tool/grep.go" {
		t.Fatalf("line=%q want @internal/tool/grep.go", string(e.line))
	}
	if e.active {
		t.Fatal("dropdown should close after accept")
	}
	// 关闭后再回车应提交
	if got := e.handleKey(ev(keyEnter)); got != statusSubmit {
		t.Fatalf("second enter status=%v want submit", got)
	}
}

// TestEditor_TabAccepts 验证 Tab 选中写回。
func TestEditor_TabAccepts(t *testing.T) {
	e := newTestCore("main.go")
	typeRunes(e, "@mai")
	e.handleKey(ev(keyTab))
	if string(e.line) != "@main.go" {
		t.Fatalf("line=%q want @main.go", string(e.line))
	}
}

// TestEditor_Navigation 验证 ↑↓ 循环导航选中项。
func TestEditor_Navigation(t *testing.T) {
	e := newTestCore("a.go", "b.go", "c.go")
	typeRunes(e, "@.go")
	if len(e.sugg) != 3 {
		t.Fatalf("n=%d want 3", len(e.sugg))
	}
	e.handleKey(ev(keyDown))
	if e.sel != 1 {
		t.Fatalf("after down sel=%d want 1", e.sel)
	}
	e.handleKey(ev(keyUp))
	e.handleKey(ev(keyUp))
	if e.sel != 2 {
		t.Fatalf("after up*2 from 1 sel=%d want 2 (wrap)", e.sel)
	}
}

// TestEditor_Esc 验证 Esc 关闭下拉但保留已输入内容。
func TestEditor_Esc(t *testing.T) {
	e := newTestCore("main.go")
	typeRunes(e, "@ma")
	e.handleKey(ev(keyEsc))
	if e.active {
		t.Fatal("esc should close dropdown")
	}
	if string(e.line) != "@ma" {
		t.Fatalf("line=%q want @ma", string(e.line))
	}
}

// TestEditor_Backspace 验证退格编辑并重算候选。
func TestEditor_Backspace(t *testing.T) {
	e := newTestCore("grep.go", "great.go")
	typeRunes(e, "@grea")
	if len(e.sugg) != 1 {
		t.Fatalf("n=%d want 1", len(e.sugg))
	}
	e.handleKey(ev(keyBackspace)) // -> @gre
	if len(e.sugg) != 2 {
		t.Fatalf("after backspace n=%d want 2", len(e.sugg))
	}
}

// TestEditor_CtrlCAndCtrlD 验证 Ctrl-C 中断、空行 Ctrl-D 结束。
func TestEditor_CtrlCAndCtrlD(t *testing.T) {
	e := newTestCore()
	if got := e.handleKey(ev(keyCtrlC)); got != statusInterrupt {
		t.Fatalf("ctrl-c status=%v want interrupt", got)
	}
	if got := e.handleKey(ev(keyCtrlD)); got != statusEOF {
		t.Fatalf("empty ctrl-d status=%v want eof", got)
	}
	typeRunes(e, "x")
	if got := e.handleKey(ev(keyCtrlD)); got != statusContinue {
		t.Fatalf("non-empty ctrl-d status=%v want continue", got)
	}
}

// sliceSource 是基于字节切片的 byteSource，用于解码单测（无终端依赖）。
type sliceSource struct {
	data  []byte
	i     int
	ungot int
}

func newSliceSource(b []byte) *sliceSource { return &sliceSource{data: b, ungot: -1} }

func (s *sliceSource) unread(b byte) { s.ungot = int(b) }

func (s *sliceSource) readByte(context.Context) (byte, error) {
	if s.ungot >= 0 {
		b := byte(s.ungot)
		s.ungot = -1
		return b, nil
	}
	if s.i >= len(s.data) {
		return 0, context.Canceled
	}
	b := s.data[s.i]
	s.i++
	return b, nil
}

func (s *sliceSource) readByteNow() (byte, bool) {
	if s.ungot >= 0 {
		b := byte(s.ungot)
		s.ungot = -1
		return b, true
	}
	if s.i >= len(s.data) {
		return 0, false
	}
	b := s.data[s.i]
	s.i++
	return b, true
}

// TestDecodeKey 验证控制字符、方向键转义与多字节 UTF-8 的解码。
func TestDecodeKey(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		typ  keyType
		r    rune
	}{
		{name: "enter cr", in: []byte{'\r'}, typ: keyEnter},
		{name: "tab", in: []byte{'\t'}, typ: keyTab},
		{name: "backspace del", in: []byte{0x7f}, typ: keyBackspace},
		{name: "ctrl-c", in: []byte{0x03}, typ: keyCtrlC},
		{name: "rune ascii", in: []byte{'a'}, typ: keyRune, r: 'a'},
		{name: "arrow up", in: []byte{0x1b, '[', 'A'}, typ: keyUp},
		{name: "arrow down", in: []byte{0x1b, '[', 'B'}, typ: keyDown},
		{name: "home seq", in: []byte{0x1b, '[', '1', '~'}, typ: keyHome},
		{name: "utf8 cjk", in: []byte{0xe4, 0xbd, 0xa0}, typ: keyRune, r: '你'},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeKey(context.Background(), newSliceSource(tc.in))
			if err != nil {
				t.Fatalf("decode err: %v", err)
			}
			if got.typ != tc.typ || (tc.typ == keyRune && got.r != tc.r) {
				t.Fatalf("got {typ=%d r=%q} want {typ=%d r=%q}", got.typ, got.r, tc.typ, tc.r)
			}
		})
	}
}

// TestDecodeLoneEsc 验证孤立 ESC（无后续字节）解码为 keyEsc。
func TestDecodeLoneEsc(t *testing.T) {
	got, err := decodeKey(context.Background(), newSliceSource([]byte{0x1b}))
	if err != nil {
		t.Fatalf("decode err: %v", err)
	}
	if got.typ != keyEsc {
		t.Fatalf("typ=%d want keyEsc", got.typ)
	}
}
