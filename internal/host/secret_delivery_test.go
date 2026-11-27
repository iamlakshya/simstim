package host

import (
	"reflect"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func secretDeliveryFixture() *api.SecretDeliveryIntent {
	return &api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1,
		Modes: map[string]api.SecretMode{"BOUND": api.SecretModeBound, "EXPOSED": api.SecretModeExposed},
		Binds: map[string]api.SecretBind{"BOUND": {Hosts: []string{"API.example.com"}, Header: "authorization", Format: "Bearer {value}"}}}
}

func TestSecretDeliveryPreservesDeclaredMissingNamesAndOwnsMetadata(t *testing.T) {
	input := secretDeliveryFixture()
	owned, names, err := PrepareSecretDelivery(input, map[string]api.Secret{"BOUND": "fixture-value"}, true, []string{"BOUND", "EXPOSED"})
	if err != nil || !reflect.DeepEqual(names, []string{"BOUND", "EXPOSED"}) || owned.Binds["BOUND"].Hosts[0] != "api.example.com:443" {
		t.Fatal("delivery dropped a declared missing name or failed canonical metadata admission")
	}
	owned.Modes["BOUND"] = api.SecretModeExposed
	owned.Binds["BOUND"].Hosts[0] = "changed"
	if input.Modes["BOUND"] != api.SecretModeBound || input.Binds["BOUND"].Hosts[0] != "API.example.com" {
		t.Fatal("publication metadata retained mutable caller storage")
	}
}

func TestSecretDeliveryRejectsUnsupportedPartitionBeforeLifecycle(t *testing.T) {
	for _, change := range []func(*api.SecretDeliveryIntent){
		func(v *api.SecretDeliveryIntent) { v.Version++ },
		func(v *api.SecretDeliveryIntent) { v.Modes["BOUND"] = "future" },
		func(v *api.SecretDeliveryIntent) { delete(v.Binds, "BOUND") },
		func(v *api.SecretDeliveryIntent) { v.Binds["EXPOSED"] = v.Binds["BOUND"] },
		func(v *api.SecretDeliveryIntent) { v.Binds["UNDECLARED"] = v.Binds["BOUND"] },
		func(v *api.SecretDeliveryIntent) { v.Modes["SSH_AUTH_SOCK"] = api.SecretModeExposed },
		func(v *api.SecretDeliveryIntent) { v.Modes["TERM"] = api.SecretModeExposed },
	} {
		intent := secretDeliveryFixture()
		change(intent)
		owned, names, err := PrepareSecretDelivery(intent, nil, true, nil)
		if err == nil || owned != nil || names != nil {
			t.Fatal("unsupported delivery returned partial metadata for a lifecycle operation")
		}
	}
	if _, _, err := PrepareSecretDelivery(secretDeliveryFixture(), map[string]api.Secret{"OTHER": "value"}, true, nil); err == nil {
		t.Fatal("undeclared actual value bypassed the delivery partition")
	}
	incomplete := secretDeliveryFixture()
	delete(incomplete.Modes, "EXPOSED")
	if _, _, err := PrepareSecretDelivery(incomplete, nil, true, []string{"BOUND", "EXPOSED"}); err == nil {
		t.Fatal("resume silently dropped an original requested mode/name")
	}
	if _, _, err := PrepareSecretDelivery(secretDeliveryFixture(), nil, true, []string{}); err == nil {
		t.Fatal("a resume with no original names gained fresh names")
	}
}

func TestSecretDeliveryNeverFallsBackToUnwalledExposure(t *testing.T) {
	intent := secretDeliveryFixture()
	if owned, names, err := PrepareSecretDelivery(intent, nil, false, nil); err == nil || owned != nil || names != nil {
		t.Fatal("bound intent became an exposed unwalled value")
	}
	delete(intent.Modes, "BOUND")
	delete(intent.Binds, "BOUND")
	if _, names, err := PrepareSecretDelivery(intent, map[string]api.Secret{"EXPOSED": "value"}, false, nil); err != nil || !reflect.DeepEqual(names, []string{"EXPOSED"}) {
		t.Fatal("explicit supported unwalled exposure lost compatibility")
	}
}
