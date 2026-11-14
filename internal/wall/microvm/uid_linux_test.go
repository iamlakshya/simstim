//go:build linux

package microvm

import "testing"

func TestHelperIdentityCannotSilentlyFallBack(t *testing.T) {
	for _, tc := range []struct {
		mode string
		uid  int
		ok   bool
	}{{"login", 0, true}, {"subuid", 1, true}, {"subuid", 0, false}, {"login", 1, false}, {"login", 1000, false}, {"unknown", 0, false}} {
		if got := checkHelperUID(tc.mode, tc.uid) == nil; got != tc.ok {
			t.Fatalf("mode%s uid%d accepted=%v", tc.mode, tc.uid, got)
		}
	}
}
