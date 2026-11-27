package supply

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func extraPin(t *testing.T, ref string, tag *string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../guest/supply.lock")
	if err != nil {
		t.Fatal(err)
	}
	quoted, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	extra := fmt.Sprintf("\n[[pin]]\nname = \"extra\"\nrepo = \"example/tool\"\nref = %s\ncommit = \"%s\"\n", quoted, strings.Repeat("a", 40))
	if tag != nil {
		quoted, err = json.Marshal(*tag)
		if err != nil {
			t.Fatal(err)
		}
		extra += "tag = " + string(quoted) + "\n"
	}
	return append(b, extra...)
}

func TestLockPinRefAndTagUsePinsWatchGrammar(t *testing.T) {
	for _, ref := range []string{"topic\x07", "topic\u034f", strings.Repeat("x", 201), "", "two words"} {
		if _, err := LoadLock(extraPin(t, ref, nil)); err == nil {
			t.Errorf("accepted invalid extra ref %q", ref)
		}
	}
	for _, ref := range []string{"v1.2.3", strings.Repeat("x", 200), "release/topic"} {
		if _, err := LoadLock(extraPin(t, ref, nil)); err != nil {
			t.Errorf("valid ref %q: %v", ref, err)
		}
	}
	for _, tag := range []string{"", "v1.2.3", "{version}{version}", "v{version}\x07", "v{version}\u034f"} {
		if _, err := LoadLock(extraPin(t, "v1.2.3", &tag)); err == nil {
			t.Errorf("accepted invalid explicit tag form %q", tag)
		}
	}
	for _, tag := range []string{"v{version}", "releases/{version}", "{version}"} {
		if _, err := LoadLock(extraPin(t, "v1.2.3", &tag)); err != nil {
			t.Errorf("valid tag form %q: %v", tag, err)
		}
	}
}

func TestLockRejectsUnsafeRuntimePins(t *testing.T) {
	b, err := os.ReadFile("../../guest/supply.lock")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, old, replacement string }{
		{"kernel outside the supported line", "6.18.65", "6.17.65"},
		{"known vulnerable VMM", "54.1", "54.0"},
		{"support ends before release", "2027-01-06", "2026-11-10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := strings.ReplaceAll(string(b), tc.old, tc.replacement)
			if changed == string(b) {
				t.Fatal("the shipped lock did not contain the test input")
			}
			if _, err := LoadLock([]byte(changed)); err == nil {
				t.Fatal("LoadLock accepted an unsafe runtime pin")
			}
		})
	}
}

func TestLockChecksKernelAgainstTheKnownBadList(t *testing.T) {
	b, err := os.ReadFile("../../guest/supply.lock")
	if err != nil {
		t.Fatal(err)
	}
	before := knownBad
	t.Cleanup(func() { knownBad = before })
	knownBad = append(KnownBad(), Advisory{ID: "test-kernel-advisory", Kind: AdvisoryKernel,
		Name: "guest", Before: "6.18.66", Severity: "high", CVSS: 8})
	if _, err := LoadLock(b); err == nil || !strings.Contains(err.Error(), "test-kernel-advisory") {
		t.Fatalf("LoadLock = %v, want the known kernel advisory", err)
	}
}
