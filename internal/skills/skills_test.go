package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/goleak"
)

// TestMain 在包级别断言无 goroutine 泄漏（DEV_SPEC §9.3）。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// writeSkill 在 <root>/.cogent/skills/<name>/SKILL.md 写入内容（测试夹具）。
func writeSkill(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, ControlDir, SkillsSubdir, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
}

func TestList_MissingDirReturnsEmpty(t *testing.T) {
	got, err := New().List(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d skills, want 0 for missing dir", len(got))
	}
}

func TestList_IndexesNameAndDescriptionSorted(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "zebra", "# Zebra skill\nbody")
	writeSkill(t, root, "add-rate-limiter", "# Add a rate limiter middleware\nstep 1...")

	got, err := New().List(context.Background(), root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d skills, want 2", len(got))
	}
	if got[0].Name != "add-rate-limiter" || got[1].Name != "zebra" {
		t.Errorf("not sorted by name: %+v", got)
	}
	if got[0].Description != "Add a rate limiter middleware" {
		t.Errorf("description = %q, want heading stripped of #", got[0].Description)
	}
	if got[0].Body != "" {
		t.Errorf("List must not load Body (lightweight index), got %q", got[0].Body)
	}
}

func TestList_SkipsBadEntries(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "valid", "# Valid\nx")
	// 非法名目录（含点）即便有 SKILL.md 也应跳过。
	bad := filepath.Join(root, ControlDir, SkillsSubdir, "bad.name")
	if err := os.MkdirAll(bad, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, FileName), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 缺 SKILL.md 的目录也应跳过。
	if err := os.MkdirAll(filepath.Join(root, ControlDir, SkillsSubdir, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := New().List(context.Background(), root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Name != "valid" {
		t.Errorf("got %+v, want only 'valid'", got)
	}
}

func TestLoad_ReturnsBody(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "deploy", "# Deploy guide\n1. build\n2. ship")

	sk, err := New().Load(context.Background(), root, "deploy")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.Contains(sk.Body, "1. build") {
		t.Errorf("body missing content: %q", sk.Body)
	}
	if sk.Description != "Deploy guide" {
		t.Errorf("description = %q", sk.Description)
	}
}

func TestLoad_InvalidNameRejected(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"../escape", "a/b", "has space", ".."} {
		if _, err := New().Load(context.Background(), root, name); err == nil {
			t.Errorf("Load(%q) should be rejected", name)
		}
	}
}

func TestLoad_MissingReturnsError(t *testing.T) {
	if _, err := New().Load(context.Background(), t.TempDir(), "nope"); err == nil {
		t.Fatal("Load of missing skill should error")
	}
}

func TestLoad_TruncatesLongBody(t *testing.T) {
	root := t.TempDir()
	big := "# Big\n" + strings.Repeat("x", MaxBodyBytes+5000)
	writeSkill(t, root, "big", big)

	sk, err := New().Load(context.Background(), root, "big")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(sk.Body) > MaxBodyBytes {
		t.Errorf("body = %d bytes, want <= %d", len(sk.Body), MaxBodyBytes)
	}
}

func TestRelevant_KeywordRecall(t *testing.T) {
	skills := []Skill{
		{Name: "add-rate-limiter", Description: "Add a rate limiter middleware"},
		{Name: "deploy", Description: "Deploy guide"},
		{Name: "logging", Description: "Structured logging setup"},
	}
	got := Relevant(skills, "how to add a rate limiter", 2)
	if len(got) == 0 || got[0].Name != "add-rate-limiter" {
		t.Errorf("top relevant = %+v, want add-rate-limiter first", got)
	}
}

func TestRelevant_EmptyQueryReturnsHead(t *testing.T) {
	skills := []Skill{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	got := Relevant(skills, "", 2)
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Errorf("got %+v, want first two in order", got)
	}
}

func TestRelevant_NoMatchReturnsNothing(t *testing.T) {
	skills := []Skill{{Name: "deploy", Description: "Deploy guide"}}
	if got := Relevant(skills, "quantum chromodynamics", 3); len(got) != 0 {
		t.Errorf("got %+v, want empty for no keyword overlap", got)
	}
}
