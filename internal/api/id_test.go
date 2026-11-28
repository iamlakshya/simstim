package api

import (
	"strings"
	"testing"
)

func TestNewSessionIDAlwaysLooksLikeID(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		id := NewSessionID()
		if !ValidSessionID(id) || !LooksLikeID(id) {
			t.Fatalf("NewSessionID() = %q, want a valid ID with a digit", id)
		}
		seen[id] = true
	}
	if len(seen) < 190 {
		t.Errorf("200 IDs had only %d distinct values", len(seen))
	}
}

func TestLooksLikeID(t *testing.T) {
	for _, yes := range []string{"abcd2345", "22222222", "aaaaaaa7", "z2bcdefg"} {
		if !LooksLikeID(yes) {
			t.Errorf("LooksLikeID(%q) = false, want true", yes)
		}
	}
	for _, no := range []string{"frontend", "abcd234", "abcd23456", "abcd-234", "ABCD2345", "abcd2348", "abcd2341", "abcd2340", "auth-fix", ""} {
		if LooksLikeID(no) {
			t.Errorf("LooksLikeID(%q) = true, want false", no)
		}
	}
}

func TestCheckName(t *testing.T) {
	for _, okName := range []string{"", "frontend", "auth-fix", "abcd-2345", strings.Repeat("a", SessionNameMax), strings.Repeat("é", SessionNameMax)} {
		if msg, hint, ok := CheckName(okName); !ok {
			t.Errorf("CheckName(%q) failed: %q / %q", okName, msg, hint)
		}
	}
	if msg, hint, ok := CheckName("  abcd2345  "); ok || !strings.Contains(msg, "session ID") || !strings.Contains(hint, "abcd-2345") {
		t.Errorf("CheckName(ID-like) = %q, %q, %v", msg, hint, ok)
	}
	if msg, hint, ok := CheckName(strings.Repeat("a", SessionNameMax+1)); ok || !strings.Contains(msg, "64") || hint == "" {
		t.Errorf("CheckName(too long) = %q, %q, %v", msg, hint, ok)
	}
}
