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

// TestStore_LoadToleratesHalfWrittenLine 验证进程崩溃留下的半行 JSON 被跳过，
// 且其后的完整行仍能正常恢复（OPTIMIZE_SPEC R5 边界显式化）。
func TestStore_LoadToleratesHalfWrittenLine(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	ctx := context.Background()
	sid := "halfline"

	if err := st.Append(ctx, sid, Event{UUID: "u1", Type: "user", Payload: payload(t, map[string]string{"text": "a"})}); err != nil {
		t.Fatalf("Append u1: %v", err)
	}
	// 模拟崩溃留下的坏行（残缺 JSON，以换行结尾自成一行），再追加一条完整事件。
	f, err := os.OpenFile(filepath.Join(dir, sid+fileSuffix), os.O_APPEND|os.O_WRONLY, filePerms)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString(`{"uuid":"broken","type":"user","payload":{"text":"` + "\n"); err != nil {
		t.Fatalf("write half line: %v", err)
	}
	_ = f.Close()
	if err := st.Append(ctx, sid, Event{UUID: "u2", Type: "assistant", Payload: payload(t, map[string]string{"text": "b"})}); err != nil {
		t.Fatalf("Append u2: %v", err)
	}

	got, err := st.Load(ctx, sid)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// 半行被跳过，u1 与 u2 仍恢复（broken 行不应进入结果）。
	if len(got) != 2 {
		t.Fatalf("loaded %d events, want 2 (half line must be skipped): %+v", len(got), got)
	}
	for _, e := range got {
		if e.UUID == "broken" {
			t.Errorf("half-written line should not be recovered: %+v", e)
		}
	}
}

// TestStore_RedactsExtendedCredentials 验证扩充后的脱敏规则覆盖 GitHub/AWS/Bearer 等高熵凭据。
func TestStore_RedactsExtendedCredentials(t *testing.T) {
	tests := []struct {
		name string
		text string
		leak string
	}{
		{"github pat", "use ghp_0123456789ABCDEFabcdef0123 now", "ghp_0123456789ABCDEFabcdef0123"},
		{"aws key", "id AKIAIOSFODNN7EXAMPLE here", "AKIAIOSFODNN7EXAMPLE"},
		{"bearer", "Authorization: Bearer abcdef0123456789ABCDEF", "abcdef0123456789ABCDEF"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			st := NewStore(dir)
			sid := "extcred"
			if err := st.Append(context.Background(), sid, Event{UUID: "u", Type: "user", Payload: payload(t, map[string]string{"text": tt.text})}); err != nil {
				t.Fatalf("Append: %v", err)
			}
			raw, err := os.ReadFile(filepath.Join(dir, sid+fileSuffix))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if strings.Contains(string(raw), tt.leak) {
				t.Errorf("credential %q leaked: %s", tt.leak, raw)
			}
		})
	}
}
