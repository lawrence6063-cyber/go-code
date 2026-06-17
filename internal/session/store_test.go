package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/goleak"
)

// TestMain 在包级别断言无 goroutine 泄漏（spec §9.3）。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// payload 构造一个简单的 JSON 载荷，便于测试。
func payload(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b
}

func TestStore_AppendLoadRoundTrip(t *testing.T) {
	st := NewStore(t.TempDir())
	ctx := context.Background()
	sid := "20260617-000000-abc123"

	events := []Event{
		{UUID: "u1", Type: "user", Payload: payload(t, map[string]string{"text": "hi"})},
		{UUID: "u2", ParentUUID: "u1", Type: "assistant", Payload: payload(t, map[string]string{"text": "yo"})},
	}
	for _, e := range events {
		if err := st.Append(ctx, sid, e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := st.Load(ctx, sid)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("loaded %d events, want 2", len(got))
	}
	if got[0].UUID != "u1" || got[1].UUID != "u2" || got[1].ParentUUID != "u1" {
		t.Errorf("event chain mismatch: %+v", got)
	}
}

func TestStore_LoadDedupByUUID(t *testing.T) {
	st := NewStore(t.TempDir())
	ctx := context.Background()
	sid := "sess-dedup"

	dup := Event{UUID: "same", Type: "user", Payload: payload(t, map[string]string{"text": "a"})}
	for i := 0; i < 3; i++ {
		if err := st.Append(ctx, sid, dup); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	got, err := st.Load(ctx, sid)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("loaded %d events after dedup, want 1", len(got))
	}
}

func TestStore_LoadMissingReturnsNotFound(t *testing.T) {
	st := NewStore(t.TempDir())
	_, err := st.Load(context.Background(), "nope")
	if err == nil || !isNotFound(err) {
		t.Fatalf("err = %v, want ErrSessionNotFound", err)
	}
}

// isNotFound 判定错误链是否包含 ErrSessionNotFound。
func isNotFound(err error) bool {
	return err != nil && (err == ErrSessionNotFound || strings.Contains(err.Error(), ErrSessionNotFound.Error()))
}

func TestStore_RedactsSecrets(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]string
		want    string // 不应出现在落盘内容中的明文
	}{
		{"api_key field", map[string]string{"api_key": "supersecretvalue"}, "supersecretvalue"},
		{"token field", map[string]string{"token": "another-secret-here"}, "another-secret-here"},
		{"sk token in text", map[string]string{"text": "key is sk-ABCDEF1234567890"}, "sk-ABCDEF1234567890"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			st := NewStore(dir)
			sid := "redact"
			if err := st.Append(context.Background(), sid, Event{UUID: "u", Type: "user", Payload: payload(t, tt.payload)}); err != nil {
				t.Fatalf("Append: %v", err)
			}
			raw, err := os.ReadFile(filepath.Join(dir, sid+fileSuffix))
			if err != nil {
				t.Fatalf("read file: %v", err)
			}
			if strings.Contains(string(raw), tt.want) {
				t.Errorf("secret %q leaked into transcript: %s", tt.want, raw)
			}
			if !strings.Contains(string(raw), "[REDACTED]") {
				t.Errorf("expected [REDACTED] marker, got: %s", raw)
			}
		})
	}
}

func TestStore_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	sid := "perm"
	if err := st.Append(context.Background(), sid, Event{UUID: "u", Type: "user"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, sid+fileSuffix))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != filePerms {
		t.Errorf("file perm = %o, want %o", perm, filePerms)
	}
}

func TestStore_ResolveAndPathTraversal(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)

	want := filepath.Join(dir, "good-id"+fileSuffix)
	if got := st.Resolve("good-id"); got != want {
		t.Errorf("Resolve = %q, want %q", got, want)
	}

	// 非法 sessionID（含路径穿越）应被拒绝：Append/Load 报错。
	for _, bad := range []string{"../evil", "a/b", "", "..", "with space"} {
		if err := st.Append(context.Background(), bad, Event{UUID: "x"}); err == nil {
			t.Errorf("Append(%q) expected error, got nil", bad)
		}
	}
}

func TestNewSessionID_Format(t *testing.T) {
	id := NewSessionID()
	if !idCharset.MatchString(id) {
		t.Errorf("session id %q contains illegal chars", id)
	}
	if NewSessionID() == id {
		t.Error("two session ids collided")
	}
}
