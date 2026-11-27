//go:build linux

package host

import "testing"

func TestOriginalBootRequiresExactNativeDeclaredCapAndNoSwap(t *testing.T) {
	m := Manifest{ID: "aaaa1111", Wall: &WallRecord{Driver: "gvisor", Level: "gvisor"}, Limits: Limits{MemoryMax: 512 << 20, Declared: 512 << 20}}
	exact := memoryUnitFactsV1{exists: true, maximum: 512 << 20, swap: 0}
	if err := validateNativeMemoryBootV1(&m, exact, true); err != nil {
		t.Fatal(err)
	}
	for _, native := range []memoryUnitFactsV1{{}, {exists: true, maximum: 256 << 20}, {exists: true, maximum: 1024 << 20}, {exists: true, maximum: 512 << 20, swap: 1}} {
		if err := validateNativeMemoryBootV1(&m, native, true); err == nil {
			t.Fatalf("unproved native unit became a boot cap: %+v", native)
		}
	}
	if err := validateNativeMemoryBootV1(&m, exact, false); err == nil {
		t.Fatal("an outside or sibling supervisor received the private boot loan")
	}
	m.Limits.Declared = 256 << 20
	if err := validateNativeMemoryBootV1(&m, exact, true); err == nil {
		t.Fatal("a smaller declared reservation authorised a larger actual boot")
	}
}
