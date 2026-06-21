package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alaindong/cogent/internal/skills"
)

func TestAugmentWithSkills_NoSkillsReturnsIntent(t *testing.T) {
	intent := "fix the parser bug"
	got := augmentWithSkills(context.Background(), t.TempDir(), intent)
	if got != intent {
		t.Errorf("got %q, want unchanged intent for empty skills dir", got)
	}
}

func TestAugmentWithSkills_InjectsRelevantBody(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, skills.ControlDir, skills.SkillsSubdir, "add-rate-limiter")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "# Add a rate limiter middleware\n1. wrap handler\n2. token bucket"
	if err := os.WriteFile(filepath.Join(dir, skills.FileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	got := augmentWithSkills(context.Background(), root, "please add a rate limiter to the api")
	if !strings.Contains(got, "skill: add-rate-limiter") {
		t.Errorf("injected text missing skill header: %q", got)
	}
	if !strings.Contains(got, "token bucket") {
		t.Errorf("injected text missing skill body: %q", got)
	}
}

func TestAugmentWithSkills_IrrelevantNotInjected(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, skills.ControlDir, skills.SkillsSubdir, "deploy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, skills.FileName), []byte("# Deploy guide\nship it"), 0o600); err != nil {
		t.Fatal(err)
	}

	intent := "refactor quantum chromodynamics solver"
	got := augmentWithSkills(context.Background(), root, intent)
	if got != intent {
		t.Errorf("irrelevant skill should not be injected; got %q", got)
	}
}
