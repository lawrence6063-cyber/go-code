package permission

import (
	"encoding/json"
	"testing"
)

func TestStaticPolicy_Evaluate(t *testing.T) {
	pol := StaticPolicy{
		Allow: map[string]bool{"read_file": true},
		Deny:  map[string]bool{"rmrf": true},
	}
	tests := []struct {
		name string
		tool string
		want Behavior
	}{
		{"allow listed", "read_file", BehaviorAllow},
		{"deny listed", "rmrf", BehaviorDeny},
		{"unknown defaults ask", "write_file", BehaviorAsk},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pol.Evaluate(tt.tool, json.RawMessage(`{}`))
			if got.Behavior != tt.want {
				t.Errorf("Evaluate(%q).Behavior = %v, want %v", tt.tool, got.Behavior, tt.want)
			}
		})
	}
}

func TestStaticPolicy_DenyTakesPrecedence(t *testing.T) {
	// 同一工具既在 Allow 又在 Deny，Deny 优先（fail-closed）。
	pol := StaticPolicy{
		Allow: map[string]bool{"x": true},
		Deny:  map[string]bool{"x": true},
	}
	if got := pol.Evaluate("x", nil); got.Behavior != BehaviorDeny {
		t.Errorf("Behavior = %v, want Deny", got.Behavior)
	}
}

func TestBehavior_String(t *testing.T) {
	tests := []struct {
		b    Behavior
		want string
	}{
		{BehaviorAllow, "allow"},
		{BehaviorDeny, "deny"},
		{BehaviorAsk, "ask"},
	}
	for _, tt := range tests {
		if got := tt.b.String(); got != tt.want {
			t.Errorf("%d.String() = %q, want %q", tt.b, got, tt.want)
		}
	}
}
