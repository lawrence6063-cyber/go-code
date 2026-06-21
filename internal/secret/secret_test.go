package secret

import (
	"strings"
	"testing"
)

func TestRedact_Tokens(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		redacted  bool // 是否应被脱敏
		mustNotEq string
	}{
		{"openai key", "key=sk-abcdef0123456789", true, "sk-abcdef0123456789"},
		{"github pat", "token ghp_0123456789ABCDEFabcdef0123", true, "ghp_0123456789ABCDEFabcdef0123"},
		{"aws akia", "AKIAIOSFODNN7EXAMPLE here", true, "AKIAIOSFODNN7EXAMPLE"},
		{"slack", "xoxb-1234567890-abcdefXYZ", true, "xoxb-1234567890-abcdefXYZ"},
		{"bearer", "Authorization: Bearer abcdef0123456789ABCDEF", true, "abcdef0123456789ABCDEF"},
		{"jwt", "eyJhbGciOiJIUzI1NiIs.eyJzdWIiOiIxMjM0NTY.SflKxwRJSMeKKF2QT4", true, "SflKxwRJSMeKKF2QT4"},
		{"plain text untouched", "the quick brown fox jumps", false, ""},
		{"short word not redacted", "token: ok", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := string(Redact([]byte(tt.in)))
			if tt.redacted {
				if !strings.Contains(out, placeholder) {
					t.Errorf("Redact(%q) = %q, want contain %q", tt.in, out, placeholder)
				}
				if tt.mustNotEq != "" && strings.Contains(out, tt.mustNotEq) {
					t.Errorf("Redact(%q) still leaks %q", tt.in, tt.mustNotEq)
				}
			} else if strings.Contains(out, placeholder) {
				t.Errorf("Redact(%q) = %q, should not redact benign text", tt.in, out)
			}
		})
	}
}

func TestRedact_JSONField(t *testing.T) {
	in := `{"api_key":"sk-secret-value-123456","model":"deepseek-chat"}`
	out := string(Redact([]byte(in)))
	if strings.Contains(out, "sk-secret-value-123456") {
		t.Errorf("api_key value leaked: %s", out)
	}
	if !strings.Contains(out, "deepseek-chat") {
		t.Errorf("benign field model dropped: %s", out)
	}
	if !strings.Contains(out, placeholder) {
		t.Errorf("expected redaction placeholder: %s", out)
	}
}

func TestRedact_PEM(t *testing.T) {
	in := "before\n-----BEGIN RSA PRIVATE KEY-----\nMIIEexample\n-----END RSA PRIVATE KEY-----\nafter"
	out := string(Redact([]byte(in)))
	if strings.Contains(out, "MIIEexample") {
		t.Errorf("PEM body leaked: %s", out)
	}
	if !strings.Contains(out, "before") || !strings.Contains(out, "after") {
		t.Errorf("surrounding text dropped: %s", out)
	}
}

func TestRedact_Empty(t *testing.T) {
	if got := Redact(nil); got != nil {
		t.Errorf("Redact(nil) = %v, want nil", got)
	}
	if got := RedactString(""); got != "" {
		t.Errorf("RedactString(\"\") = %q, want empty", got)
	}
}
