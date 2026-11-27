package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func protectedTrustedIntentFixtureV1() api.SecretDeliveryIntent {
	use := api.TrustedSecretUseV1{Version: 1, Use: api.TrustedJudgeUseV1, Mode: api.SecretModeBound, Bind: api.SecretBind{Hosts: []string{"judge.example:443"}, Header: "Authorization", Format: "Bearer {value}"}, Model: "example-model", Endpoint: "https://judge.example/v1/chat/completions"}
	return api.SecretDeliveryIntent{Version: 1, Modes: map[string]api.SecretMode{}, TrustedUses: map[string]api.TrustedSecretUseV1{"KEY": use}}
}

func TestProtectedTrustedRoleDoesNotAddGuestNamesOrBinding(t *testing.T) {
	intent := protectedTrustedIntentFixtureV1()
	intent.Modes["GUEST"] = api.SecretModeExposed
	owned, names, err := PrepareSecretDelivery(&intent, map[string]api.Secret{"GUEST": "guest-value"}, true, []string{"GUEST"})
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "GUEST" || len(owned.Modes) != 1 || len(owned.Binds) != 0 || len(owned.TrustedUses) != 1 {
		t.Fatal("trusted role changed the guest name or binding domain")
	}
	use := owned.TrustedUses["KEY"]
	use.Bind.Hosts[0] = "changed.example:443"
	owned.TrustedUses["KEY"] = use
	if intent.TrustedUses["KEY"].Bind.Hosts[0] != "judge.example:443" {
		t.Fatal("role canonicalisation borrowed the caller's binding")
	}
}

func TestProtectedTrustedRoleRejectsUnwalledAndUnsupportedMetadata(t *testing.T) {
	for _, variant := range []string{"unwalled", "version", "use", "mode", "endpoint", "binding", "reserved"} {
		t.Run(variant, func(t *testing.T) {
			intent := protectedTrustedIntentFixtureV1()
			use := intent.TrustedUses["KEY"]
			walled := true
			switch variant {
			case "unwalled":
				walled = false
			case "version":
				use.Version = 2
			case "use":
				use.Use = "other-use"
			case "mode":
				use.Mode = api.SecretModeExposed
			case "endpoint":
				use.Endpoint = "http://judge.example/v1/chat/completions"
			case "binding":
				use.Bind.Hosts = []string{"other.example:443"}
			case "reserved":
				delete(intent.TrustedUses, "KEY")
				intent.TrustedUses["SSH_AUTH_SOCK"] = use
			}
			if variant != "reserved" {
				intent.TrustedUses["KEY"] = use
			}
			if _, _, err := PrepareSecretDelivery(&intent, nil, walled, []string{}); err == nil {
				t.Fatal("unsupported trusted role reached publication")
			}
		})
	}
}

func TestProtectedTrustedProcessCopiesValuesAndAllowsAbsentUnarmedRole(t *testing.T) {
	intent := protectedTrustedIntentFixtureV1()
	values := map[string]api.Secret{"KEY": "private-marker"}
	process, err := captureProtectedTrustedProcessV1(&intent, values)
	if err != nil {
		t.Fatal(err)
	}
	values["KEY"] = "changed"
	if process.Values["KEY"] != "private-marker" || len(process.Uses) != 1 {
		t.Fatal("private carrier borrowed or lost its original value")
	}
	if absent, err := captureProtectedTrustedProcessV1(&intent, nil); err != nil || len(absent.Values) != 0 || len(absent.Uses) != 1 {
		t.Fatal("public-only cold role became a value or guest use", err)
	}
	if _, err := captureProtectedTrustedProcessV1(&intent, map[string]api.Secret{"EXTRA": "private-marker"}); err == nil {
		t.Fatal("an unselected protected value entered the carrier")
	}
	carrier := &protectedTrustedStartupV1{primary: process, members: map[string]protectedTrustedProcessV1{"member22": process}}
	raw, _ := json.Marshal(initialSecretState(&intent))
	if strings.Contains(string(raw), "private-marker") {
		t.Fatal("private carrier value entered the saved public state")
	}
	carrier.clearValues()
	if len(process.Values) != 0 || len(carrier.members) != 0 {
		t.Fatal("owned private maps were not cleared")
	}
}

func TestProtectedTrustedDataCannotCreateANativeStartSource(t *testing.T) {
	intent := protectedTrustedIntentFixtureV1()
	args := NewArgs{ID: "primary2", SecretDelivery: &intent, ProtectedTrustedSecretValues: map[string]api.Secret{"KEY": "private-marker"}}
	m := &Manifest{ID: args.ID, Wall: &WallRecord{Driver: "chv"}, SecretState: initialSecretState(&intent)}
	for _, operation := range []*SessionOperation{nil, {}, {id: args.ID}} {
		if err := captureNewProtectedTrustedSecretsV1(context.Background(), operation, m, args); err == nil {
			t.Fatal("New metadata manufactured a native private carrier")
		}
	}
	if _, _, err := (&SessionOperation{}).takeProtectedTrustedSecretsV1(t.Context(), &intent, nil); err == nil {
		t.Fatal("a raw operation manufactured native delivery")
	}
}
