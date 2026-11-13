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
