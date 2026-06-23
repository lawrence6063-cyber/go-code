package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseKV(t *testing.T) {
	cases := []struct {
		line    string
		key     string
		val     string
		wantOK  bool
	}{
		{"DEEPSEEK_API_KEY=sk-123", "DEEPSEEK_API_KEY", "sk-123", true},
		{"KEY=value with spaces", "KEY", "value with spaces", true}, // 无引号：整行作为值
		{"K=\"quoted value\"", "K", "quoted value", true},
		{"K='single quoted'", "K", "single quoted", true},
		{"# comment", "", "", false},
		{"", "", "", false},
		{"NO_EQUALS", "", "", false},
	}
	for _, c := range cases {
		k, v, ok := parseKV(c.line)
		if ok != c.wantOK {
			t.Errorf("parseKV(%q) ok=%v want %v", c.line, ok, c.wantOK)
			continue
		}
		if ok && (k != c.key || v != c.val) {
			t.Errorf("parseKV(%q) = (%q,%q) want (%q,%q)", c.line, k, v, c.key, c.val)
		}
	}
}

func TestLoadMissingFile(t *testing.T) {
	// 默认路径不存在时返回 0,nil
	tmp := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmp)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	n, err := Load()
	if err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if n != 0 {
		t.Fatalf("Load on missing file returned %d, want 0", n)
	}
}

func TestLoadOnlySetsMissingEnv(t *testing.T) {
	tmp := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmp)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	// 写一个测试配置
	dir := filepath.Join(tmp, ".cogent")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "DEEPSEEK_API_KEY=sk-from-file\nCOGENT_MODEL=deepseek-chat\n"
	if err := os.WriteFile(filepath.Join(dir, "config.env"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	// 预设一个已有的环境变量，验证它不被覆盖
	t.Setenv("DEEPSEEK_API_KEY", "sk-from-env")
	t.Setenv("COGENT_MODEL", "")

	n, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if n != 1 {
		t.Fatalf("Load returned %d, want 1 (only COGENT_MODEL was unset)", n)
	}
	if got := os.Getenv("DEEPSEEK_API_KEY"); got != "sk-from-env" {
		t.Errorf("DEEPSEEK_API_KEY = %q, want %q (env should win)", got, "sk-from-env")
	}
	if got := os.Getenv("COGENT_MODEL"); got != "deepseek-chat" {
		t.Errorf("COGENT_MODEL = %q, want %q (from file)", got, "deepseek-chat")
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmp)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	entries := map[string]string{
		"DEEPSEEK_API_KEY":     "sk-test-123",
		"COGENT_LLM_BASE_URL": "https://api.deepseek.com/v1",
		"COGENT_LOG_LEVEL":    "debug",
	}
	if err := SaveFile(entries); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}

	// 清掉 env 后 Load 应该能读回
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("COGENT_LLM_BASE_URL", "")
	t.Setenv("COGENT_LOG_LEVEL", "")

	n, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if n != 3 {
		t.Fatalf("Load returned %d, want 3", n)
	}
	if got := os.Getenv("DEEPSEEK_API_KEY"); got != "sk-test-123" {
		t.Errorf("DEEPSEEK_API_KEY = %q, want sk-test-123", got)
	}
}
