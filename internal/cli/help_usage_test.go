package cli

import (
	"strings"
	"testing"
)

// The last-session address is visible in both the full help and an arity
// error, as well as working in TestAddressConceptIsTrue.
func TestFlipHelpNamesTheLastSession(t *testing.T) {
	if help := ok(t, "flip", "--help"); !strings.Contains(help, "flip [<host>/<session>|-]") {
		t.Fatalf("flip help hides the last-session address:\n%s", help)
	}
	if got, code := lineErr(t, "flip", "one", "two"); code != 2 || !strings.Contains(got, "flip takes [<host>/<session>|-]") {
		t.Fatalf("flip arity error = %d:\n%s", code, got)
	}
}

// The examples use one host/session pair across all command pages. This
// checks the same parser that inspects every real example in the tree.
func TestSessionExamplesUseTheCanonicalAddress(t *testing.T) {
	for _, command := range []string{"ports", "attach", "flip", "kill"} {
		for _, address := range []string{"my-vps/web", "other/auth-fix", "box/session"} {
			if err := exampleParses([]string{command, address}); err == "" {
				t.Errorf("%s %s passed the canonical example check", command, address)
			}
		}
		if err := exampleParses([]string{command, "my-vps/auth-fix"}); err != "" {
			t.Errorf("canonical %s example failed: %s", command, err)
		}
	}
	if err := exampleParses([]string{"debug", "bundle", "my-vps", "--ssh"}); err != "" {
		t.Errorf("a documented bare-host alternative failed: %s", err)
	}
}

// Host flags and positional hosts use the same canonical name. Provider
// choices and the default names of created hosts remain valid examples.
func TestHostExamplesUseTheCanonicalAddress(t *testing.T) {
	for _, args := range [][]string{
		{"setup", "--host", "my-server", "--agents", "claude,codex"},
		{"setup", "--host=other", "--agents", "claude"},
		{"ls", "--host", "box"},
		{"agents", "install", "claude", "--host=other"},
		{"host", "test", "other"},
		{"host", "doctor", "box", "--json"},
		{"host", "alerts", "log", "other"},
		{"new", "other", "--detach", "--", "htop"},
		{"host", "idle", "other", "30m"},
	} {
		if err := exampleParses(args); !strings.Contains(err, "a host example must use my-vps") {
			t.Errorf("non-canonical host example %q = %q", args, err)
		}
	}
	for _, args := range [][]string{
		{"setup", "--host", "my-vps", "--agents", "claude,codex"},
		{"ls", "--host=my-vps"},
		{"agents", "install", "claude", "--host", "my-vps"},
		{"host", "test", "my-vps"},
		{"host", "doctor", "my-vps", "--json"},
		{"new", "my-vps", "--detach", "--", "htop"},
		{"host", "create", "docker", "test", "--image", "debian:stable-slim"},
		{"host", "create", "e2b", "cloud", "--template", "base"},
		{"host", "destroy", "lima"},
		{"host", "idle", "lima", "30m"},
	} {
		if err := exampleParses(args); err != "" {
			t.Errorf("valid host or provider example %q = %q", args, err)
		}
	}
}
