//go:build linux

package chv

import "testing"

func TestNoDumpPolicyNeverAssumesNonDefaultHostSetting(t *testing.T) {
	if err := noDumpPolicy([]byte("0\n")); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"1", "2", "", "unknown", "0\n2"} {
		if err := noDumpPolicy([]byte(value)); err == nil {
			t.Fatalf("host policy%q silently weakened VMM NoDump", value)
		}
	}
}
