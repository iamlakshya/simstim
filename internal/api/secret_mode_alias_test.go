package api

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/secretmode"
)

// Compile-time assignment is deliberate. A newly defined API wrapper type
// instead of an alias would make these identical-map assignments fail.
func aliasModesToLow(input map[string]SecretMode) map[string]secretmode.SecretMode { return input }
func aliasModesToAPI(input map[string]secretmode.SecretMode) map[string]SecretMode { return input }

func TestSecretModeAPIAliasPreservesTypeMapAndMethodIdentity(t *testing.T) {
	apiType, lowType := reflect.TypeFor[SecretMode](), reflect.TypeFor[secretmode.SecretMode]()
	if apiType != lowType || apiType.Kind() != reflect.String || apiType.Name() != "SecretMode" {
		t.Fatal("API mode is a distinct type or lost its scalar descriptor")
	}
	if apiType.PkgPath() != "github.com/iamlakshya/simstim/internal/secretmode" {
		t.Fatal("canonical enum did not move below api/proto")
	}
	modes := map[string]SecretMode{"KEY": SecretModeBound}
	low := aliasModesToLow(modes)
	low["KEY"] = secretmode.SecretModeExposed
	back := aliasModesToAPI(low)
	if modes["KEY"] != SecretModeExposed || back["KEY"] != SecretModeExposed || !back["KEY"].Valid() {
		t.Fatal("alias required copying/conversion or lost its method")
	}
}

func TestSecretModeDescriptorsAndReflectCloneKeepNamedScalarSemantics(t *testing.T) {
	input := map[string]SecretMode{"KEY": SecretModeBound}
	copyValue := cloneLayerData(reflect.ValueOf(input))
	cloned, ok := copyValue.Interface().(map[string]secretmode.SecretMode)
	if !ok || cloned["KEY"] != secretmode.SecretModeBound {
		t.Fatal("generic reflector changed alias map identity")
	}
	cloned["KEY"] = secretmode.SecretModeExposed
	if input["KEY"] != SecretModeBound {
		t.Fatal("reflect clone retained mutable map ownership")
	}
	field, ok := reflect.TypeFor[proto.CtlStartArgs]().FieldByName("SecretModes")
	if !ok || field.Type.Kind() != reflect.Map || field.Type.Elem() != reflect.TypeFor[SecretMode]() {
		t.Fatal("protocol mode descriptor differs from API alias")
	}
	valid := []byte(`{"version":1,"modes":{"KEY":"bound"},"binds":{}}`)
	if !ExactJSONFields(valid, SecretDeliveryIntent{}) {
		t.Fatal("exact JSON structural descriptor rejected named string mode")
	}
	var intent SecretDeliveryIntent
	if err := json.Unmarshal(valid, &intent); err != nil || !intent.Modes["KEY"].Valid() {
		t.Fatal("typed intent no longer accepts canonical enum")
	}
	invalid := []byte(`{"version":1,"modes":{"KEY":"future"},"binds":{}}`)
	if err := json.Unmarshal(invalid, &intent); err != nil || intent.Modes["KEY"].Valid() {
		t.Fatal("alias introduced an unknown semantic mode")
	}
	if SecretMode([]byte("bound")) != SecretModeBound {
		t.Fatal("bounded scalar projection cast changed")
	}
}

func TestSecretModeAliasCrossesActualCtlMapWithoutLosingOtherFields(t *testing.T) {
	modes := map[string]SecretMode{"KEY": SecretModeBound}
	args := proto.CtlStartArgs{Session: "store234", InputNonce: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", InputDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", SecretModes: modes}
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	var result proto.CtlStartArgs
	if err := json.Unmarshal(encoded, &result); err != nil || result.SecretModes["KEY"] != secretmode.SecretModeBound || result.InputNonce != args.InputNonce || result.InputDigest != args.InputDigest {
		t.Fatal("mode correction lost current input-phase fields or mode identity")
	}
	args.SecretModes = map[string]SecretMode{}
	empty, err := json.Marshal(args)
	if err != nil || !bytes.Contains(empty, []byte(`"secret_modes":{}`)) {
		t.Fatal("explicit empty mode map collapsed during alias migration")
	}
	args.SecretModes = nil
	legacy, err := json.Marshal(args)
	if err != nil || bytes.Contains(legacy, []byte(`"secret_modes"`)) {
		t.Fatal("legacy absent mode form changed")
	}
}
