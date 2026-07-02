package main

import "testing"

func feedMenuKeys(m *menuModel, keys ...keyEvent) menuStatus {
	last := menuContinue
	for _, k := range keys {
		last = m.handleKey(k)
	}
	return last
}

func TestMenuModelMoveWraps(t *testing.T) {
	m := newMenuModel([]string{"a", "b", "c"})
	if m.sel != 0 {
		t.Fatalf("initial sel = %d, want 0", m.sel)
	}
	m.handleKey(ev(keyDown))
	m.handleKey(ev(keyDown))
	if m.sel != 2 {
		t.Fatalf("after 2 down sel = %d, want 2", m.sel)
	}
	m.handleKey(ev(keyDown)) // 循环回首项
	if m.sel != 0 {
		t.Fatalf("wrap down sel = %d, want 0", m.sel)
	}
	m.handleKey(ev(keyUp)) // 循环到末项
	if m.sel != 2 {
		t.Fatalf("wrap up sel = %d, want 2", m.sel)
	}
}

func TestMenuModelConfirm(t *testing.T) {
	m := newMenuModel([]string{"Approve", "Reject"})
	st := feedMenuKeys(m, ev(keyDown), ev(keyEnter))
	if st != menuConfirm {
		t.Fatalf("status = %v, want menuConfirm", st)
	}
	if m.sel != 1 {
		t.Fatalf("sel = %d, want 1", m.sel)
	}
}

func TestMenuModelCancelAndInterrupt(t *testing.T) {
	m := newMenuModel([]string{"a", "b"})
	if st := m.handleKey(ev(keyEsc)); st != menuCancel {
		t.Fatalf("Esc status = %v, want menuCancel", st)
	}
	if st := m.handleKey(ev(keyCtrlG)); st != menuCancel {
		t.Fatalf("Ctrl-G status = %v, want menuCancel", st)
	}
	if st := m.handleKey(ev(keyCtrlC)); st != menuInterrupt {
		t.Fatalf("Ctrl-C status = %v, want menuInterrupt", st)
	}
}

func TestMenuModelIgnoresOtherKeys(t *testing.T) {
	m := newMenuModel([]string{"a", "b"})
	if st := m.handleKey(runeEvent('x')); st != menuContinue {
		t.Fatalf("rune status = %v, want menuContinue", st)
	}
	if m.sel != 0 {
		t.Fatalf("rune should not move selection, sel=%d", m.sel)
	}
}

func TestMenuModelEmptyOptions(t *testing.T) {
	m := newMenuModel(nil)
	m.handleKey(ev(keyDown)) // 不应 panic
	if m.sel != 0 {
		t.Fatalf("empty menu sel = %d, want 0", m.sel)
	}
}
