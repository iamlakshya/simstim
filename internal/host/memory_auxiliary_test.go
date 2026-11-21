package host

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

func auxiliaryMemoryFixtureV1(now time.Time) Manifest {
	return Manifest{ID: "aaaa2222", Wall: &WallRecord{Level: "microvm", Driver: "chv"}, Run: Runtime{BootID: "old-ordinary-boot"}, Status: Status{State: StateStarting, Since: now}, Limits: Limits{MemoryMax: 4 << 30, Declared: 4 << 30}, Auxiliary: &AuxiliaryReservationV1{Version: 1, Token: strings.Repeat("a", 32), BootID: "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", Since: now, Declared: 512 << 20}}
}

func TestAuxiliaryAccountingUsesOwnBootAndCompleteLiveCapOnce(t *testing.T) {
	now := time.Now().UTC()
	m := auxiliaryMemoryFixtureV1(now)
	native := memoryUnitFactsV1{exists: true, maximum: 512 << 20, swap: 0, current: 1}
	m.Auxiliary.Since = now.Add(-time.Hour)
	charge, active, err := auxiliaryReservationChargeV1(&m, m.Auxiliary.BootID, now, memoryUnitFactsV1{}, native)
	if err != nil || !active || charge != 512<<20 {
		t.Fatal("ordinary boot/idle RSS/old since freed complete auxiliary cap", charge, active, err)
	}
	for _, offset := range []time.Duration{-2 * time.Minute, time.Hour} {
		m.Auxiliary.Since = now.Add(offset)
		charge, active, err = auxiliaryReservationChargeV1(&m, m.Auxiliary.BootID, now, memoryUnitFactsV1{}, memoryUnitFactsV1{})
		if err != nil || !active || charge != 512<<20 {
			t.Fatal("exact2min/future auxiliary reservation was released", offset, charge, active, err)
		}
	}
	m.Auxiliary.Since = now.Add(-2*time.Minute - time.Nanosecond)
	if charge, active, err = auxiliaryReservationChargeV1(&m, m.Auxiliary.BootID, now, memoryUnitFactsV1{}, memoryUnitFactsV1{}); err != nil || active || charge != 0 {
		t.Fatal("expired unlaunched auxiliary counted", charge, active, err)
	}
}

func TestAuxiliaryAccountingRefusesOrphanOverlapFutureAndUnderstatement(t *testing.T) {
	now := time.Now().UTC()
	m := auxiliaryMemoryFixtureV1(now)
	boot := m.Auxiliary.BootID
	for _, native := range []memoryUnitFactsV1{{exists: true, maximum: 256 << 20}, {exists: true, maximum: 1024 << 20}, {exists: true, maximum: 512 << 20, swap: 1}} {
		if _, _, err := auxiliaryReservationChargeV1(&m, boot, now, memoryUnitFactsV1{}, native); err == nil {
			t.Fatal("an unproved complete native aux cap passed")
		}
	}
	if _, _, err := auxiliaryReservationChargeV1(&m, boot, now, memoryUnitFactsV1{exists: true}, memoryUnitFactsV1{}); err == nil {
		t.Fatal("a pending auxiliary overlapped ordinary native unit")
	}
	m.Auxiliary = nil
	if _, _, err := auxiliaryReservationChargeV1(&m, boot, now, memoryUnitFactsV1{}, memoryUnitFactsV1{exists: true, maximum: 512 << 20}); err == nil {
		t.Fatal("orphan native auxiliary became zero")
	}
	var future AuxiliaryReservationV1
	if err := json.Unmarshal([]byte(`{"v":2,"future":{"kept":true}}`), &future); err != nil {
		t.Fatal(err)
	}
	m.Auxiliary = &future
	if _, _, err := auxiliaryReservationChargeV1(&m, boot, now, memoryUnitFactsV1{}, memoryUnitFactsV1{}); err == nil {
		t.Fatal("future data promoted current accounting")
	}
	copied := CloneManifest(m)
	copied.Auxiliary.opaque[0] = '['
	if string(m.Auxiliary.opaque) != `{"v":2,"future":{"kept":true}}` {
		t.Fatal("manifest clone borrowed future opaque reservation bytes")
	}
}

func TestAuxiliaryUnitNamesAreClosedAndDataCannotMintAdmission(t *testing.T) {
	name, err := AuxUnitNameV1("aaaa2222")
	if err != nil || name != brand.UnitPrefix+"aaaa2222-aux.service" {
		t.Fatal("wrong closed auxiliary identity", name, err)
	}
	key, err := canonicalMemoryUnitKeyV1(name)
	if err != nil || key != auxiliaryMemoryKeyV1("aaaa2222") {
		t.Fatal("native auxiliary was not enumerated", key, err)
	}
	for _, bad := range []string{brand.UnitPrefix + "aaaa2222-any.service", brand.UnitPrefix + "../outside-aux.service", brand.UnitPrefix + "aaaa2222-aux.scope"} {
		if _, err := canonicalMemoryUnitKeyV1(bad); err == nil {
			t.Fatal("an unsupported branded unit became ordinary capacity", bad)
		}
	}
}
