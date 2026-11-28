package api

import (
	"strings"
	"testing"
)

func TestParseSandboxSystem(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"", SandboxSystemReset},
		{"reset", SandboxSystemReset},
		{"keep", SandboxSystemKeep},
	} {
		got, err := ParseSandboxSystem(tc.in)
		if err != nil {
			t.Fatalf("ParseSandboxSystem(%q) error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ParseSandboxSystem(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	if _, err := ParseSandboxSystem("persist"); err == nil {
		t.Fatal("expected invalid sandbox.system to fail")
	} else if !strings.Contains(err.Error(), "Try:") {
		t.Errorf("error missing Try: hint: %v", err)
	}
}
