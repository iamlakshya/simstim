package secretmode

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSecretModeCanonicalKindNameAndExactClosedValues(t *testing.T) {
	typ := reflect.TypeFor[SecretMode]()
	if typ.Kind() != reflect.String || typ.Name() != "SecretMode" {
		t.Fatal("canonical mode lost its named string representation")
	}
	for _, mode := range []SecretMode{SecretModeBound, SecretModeExposed} {
		if !mode.Valid() {
			t.Fatal("supported canonical mode refused")
		}
		encoded, err := json.Marshal(mode)
		if err != nil || string(encoded) != `"`+string(mode)+`"` {
			t.Fatal("mode wire spelling changed")
		}
		var decoded SecretMode
		if err := json.Unmarshal(encoded, &decoded); err != nil || decoded != mode || !decoded.Valid() {
			t.Fatal("canonical mode did not round-trip")
		}
	}
	for _, mode := range []SecretMode{"", "BOUND", "Exposed", "bound ", " exposed", "future", "bound\x00"} {
		if mode.Valid() {
			t.Fatal("unsupported delivery mode was accepted")
		}
	}
	if string(SecretModeBound) != "bound" || string(SecretModeExposed) != "exposed" {
		t.Fatal("canonical wire values changed")
	}
}
