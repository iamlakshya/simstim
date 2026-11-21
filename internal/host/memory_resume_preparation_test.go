package host

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func resumePreparationFixtureV1() Manifest {
	return Manifest{ID: "aaaa2222", Wall: &WallRecord{Level: "microvm", Driver: "chv"}, Run: Runtime{BootID: "previous-ordinary-boot"}, Status: Status{State: StateStarting, Since: time.Now().UTC().Add(-time.Hour)}, Limits: Limits{MemoryMax: 512 << 20, Declared: 512 << 20}, ResumePreparation: &ResumePreparationV1{V: 1, Nonce: strings.Repeat("a", 64), Boot: "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", Since: time.Now().UTC().Add(-24 * time.Hour), MemoryMax: 512 << 20}}
}
func TestHeldPreparationHasNoExpiryAndCountsNativeQuantumOnce(t *testing.T) {
	m := resumePreparationFixtureV1()
	for _, native := range []memoryUnitFactsV1{{}, {exists: true, maximum: 512 << 20, swap: 0}} {
		charge, active, err := resumePreparationChargeV1(&m, m.ResumePreparation.Boot, native, memoryUnitFactsV1{})
		if err != nil || !active || charge != 512<<20 {
			t.Fatal("held preparation lost or doubled its complete charge", charge, active, err)
		}
	}
	m.ResumePreparation.Boot = "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"
	if charge, active, err := resumePreparationChargeV1(&m, "new-boot", memoryUnitFactsV1{}, memoryUnitFactsV1{}); err != nil || active || charge != 0 {
		t.Fatal("old-boot absent preparation consumed current capacity", charge, active, err)
	}
	if _, _, err := resumePreparationChargeV1(&m, "new-boot", memoryUnitFactsV1{exists: true, maximum: 512 << 20}, memoryUnitFactsV1{}); err == nil {
		t.Fatal("foreign current native unit adopted an old preparation")
	}
}
func TestPreparationRefusesUnderstatementAndAuxiliaryOverlap(t *testing.T) {
	m := resumePreparationFixtureV1()
	cases := []struct{ primary, aux memoryUnitFactsV1 }{
		{primary: memoryUnitFactsV1{exists: true, maximum: 1 << 30}},
		{primary: memoryUnitFactsV1{exists: true, maximum: 512 << 20, swap: 1}},
		{aux: memoryUnitFactsV1{exists: true, maximum: 512 << 20}},
	}
	for _, test := range cases {
		if _, _, err := resumePreparationChargeV1(&m, m.ResumePreparation.Boot, test.primary, test.aux); err == nil {
			t.Fatal("uncertain native accounting became available room")
		}
	}
	m.ResumePreparation.MemoryMax = 256 << 20
	if _, _, err := resumePreparationChargeV1(&m, m.ResumePreparation.Boot, memoryUnitFactsV1{}, memoryUnitFactsV1{}); err == nil {
		t.Fatal("preparation understated protected complete cap")
	}
}
func TestPreparationFutureDataIsOwnedOpaqueAndNeverCurrentAccounting(t *testing.T) {
	raw := []byte(`{"v":2,"future":{"kept":true}}`)
	var value ResumePreparationV1
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	m := resumePreparationFixtureV1()
	m.ResumePreparation = &value
	copy := CloneManifest(m)
	copy.ResumePreparation.opaque[0] = '['
	if string(value.opaque) != string(raw) {
		t.Fatal("manifest clone aliased future preparation bytes")
	}
	if _, _, err := resumePreparationChargeV1(&m, m.ResumePreparation.Boot, memoryUnitFactsV1{}, memoryUnitFactsV1{}); err == nil {
		t.Fatal("future data became known capacity")
	}
	encoded, err := json.Marshal(value)
	if err != nil || string(encoded) != string(raw) {
		t.Fatal("opaque future data lost its owned bytes", string(encoded), err)
	}
}
