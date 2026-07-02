package completion

import (
	"testing"
)

func TestParseSlashToken(t *testing.T) {
	cases := []struct {
		name       string
		line       string
		cursor     int
		wantActive bool
		wantPart   string
	}{
		{"prefix", "/un", 3, true, "/un"},
		{"bare-slash", "/", 1, true, "/"},
		{"not-slash", "@foo", 4, false, ""},
		{"space-enters-args", "/model x", 8, false, ""},
		{"cursor-in-name", "/help", 3, true, "/he"},
		{"empty", "", 0, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tok := ParseSlashToken([]rune(c.line), c.cursor)
			if tok.Active != c.wantActive {
				t.Fatalf("Active = %v, want %v", tok.Active, c.wantActive)
			}
			if tok.Active && tok.Partial != c.wantPart {
				t.Fatalf("Partial = %q, want %q", tok.Partial, c.wantPart)
			}
		})
	}
}

func TestApplySlashChoice(t *testing.T) {
	line, cur := ApplySlashChoice([]rune("/un"), "/undo")
	if string(line) != "/undo" || cur != 5 {
		t.Fatalf("got %q cursor=%d, want %q cursor=5", string(line), cur, "/undo")
	}
	// 保留命令名之后的参数。
	line2, _ := ApplySlashChoice([]rune("/mod arg"), "/model")
	if string(line2) != "/model arg" {
		t.Fatalf("args not preserved: %q", string(line2))
	}
}

func TestCommandProviderFilter(t *testing.T) {
	p := NewCommandProvider()
	got := p.Filter("/u", 0)
	if len(got) != 1 || got[0].Name != "/undo" {
		t.Fatalf("filter /u = %+v, want single /undo", got)
	}
	all := p.Filter("/", 0)
	if len(all) == 0 {
		t.Fatalf("prefix / should match all commands")
	}
	none := p.Filter("/zzz", 0)
	if len(none) != 0 {
		t.Fatalf("no command should match /zzz, got %+v", none)
	}
}
