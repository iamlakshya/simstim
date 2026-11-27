package host

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func auxiliaryReservationFixtureV1() AuxiliaryReservationV1 {
	return AuxiliaryReservationV1{Version: 1, Token: strings.Repeat("a", 32), BootID: "12345678-1234-1234-1234-123456789abc", Since: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Declared: api.MinMemoryMax}
}
func TestAuxiliaryAccountingDataRejectsAliasesUnknownAndInvalidEpoch(t *testing.T) {
	original := auxiliaryReservationFixtureV1()
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"token":`, `"Token":`, 1),
		strings.Replace(string(raw), `"v":1`, `"v":1,"v":1`, 1),
		strings.Replace(string(raw), `"v":1`, `"v":1,"authority":true`, 1),
		strings.Replace(string(raw), `"v":1`, `"v":"1"`, 1),
		strings.Replace(string(raw), original.Token, "../caller", 1),
		strings.Replace(string(raw), original.BootID, "copied-clock", 1),
	} {
		var decoded AuxiliaryReservationV1
		if json.Unmarshal([]byte(bad), &decoded) == nil {
			t.Fatal("invalid auxiliary data entered accounting", bad)
		}
	}
	if err := validateAuxiliaryReservationV1Data(&original); err != nil {
		t.Fatal(err)
	}
	var round AuxiliaryReservationV1
	if err := json.Unmarshal(raw, &round); err != nil || round.Token != original.Token || round.Declared != original.Declared {
		t.Fatal("valid accounting data changed", err)
	}
	if cloneAuxiliaryReservationV1(nil) != nil {
		t.Fatal("nil auxiliary data became a reservation")
	}
}
func TestFutureAuxiliaryReservationIsOpaqueOwnedAndNeverAdmitted(t *testing.T) {
	raw := []byte(`{"v":2,"future":{ "unknown" : "retained" }}`)
	var decoded AuxiliaryReservationV1
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := validateAuxiliaryReservationV1Data(&decoded); err == nil {
		t.Fatal("future data became current auxiliary authority")
	}
	copy := cloneAuxiliaryReservationV1(&decoded)
	copy.opaque[0] = 'x'
	if bytes.Equal(copy.opaque, decoded.opaque) {
		t.Fatal("clone retained mutable opaque storage")
	}
	encoded, err := decoded.MarshalJSON()
	if err != nil || !bytes.Equal(encoded, raw) {
		t.Fatal("opaque payload was interpreted or lost", err)
	}
	raw[1] = 'x'
	if bytes.Equal(raw, decoded.opaque) {
		t.Fatal("decoder retained caller-owned opaque storage")
	}
}
