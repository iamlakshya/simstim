package gitpolicy

import "testing"

func TestWindowsHookGateRequiresEffectiveEventSuppression(t *testing.T) {
	for _, test := range []struct {
		version       string
		unix, windows bool
	}{{"git version 2.53.0", true, false}, {"git version 2.54.1", false, false},
		{"git version 2.55.0.windows.1", true, true}, {"git version 2.56.1", true, true}, {"unknown", false, false}} {
		if _, got := hookVersionPlatform(test.version, false); got != test.unix {
			t.Fatalf("Unix hook policy for %q = %v", test.version, got)
		}
		if _, got := hookVersionPlatform(test.version, true); got != test.windows {
			t.Fatalf("Windows hook policy for %q = %v", test.version, got)
		}
	}
}
