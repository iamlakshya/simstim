package egress

import (
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func projectionUnionFixtureV1() *api.SessionSecretState {
	bind := api.SecretBind{Hosts: []string{"api.example.test:443"}, Header: "Authorization", Format: "Bearer {value}"}
	return &api.SessionSecretState{Version: 1, Generation: strings.Repeat("a", 32),
		Intent: api.SecretDeliveryIntent{Version: 1, Modes: map[string]api.SecretMode{"LOCAL": api.SecretModeBound, "FOREIGN": api.SecretModeExposed}, Binds: map[string]api.SecretBind{"LOCAL": bind},
			TrustedUses: map[string]api.TrustedSecretUseV1{"JUDGE_ONLY": {Version: 1, Use: api.TrustedJudgeUseV1, Mode: api.SecretModeBound, Bind: api.SecretBind{Hosts: []string{"judge.example.test:443"}, Header: "Authorization", Format: "Bearer {value}"}, Model: "example-model", Endpoint: "https://judge.example.test/v1/chat/completions"}}},
		Primary: map[string]api.PublicSecretState{"LOCAL": {Mode: api.SecretModeBound, Sentinel: "STND1111", Identity: strings.Repeat("b", 32), Armed: true, Shape: &api.SecretShape{Version: 1, Slots: []api.SecretShapeSlot{{Class: "ascii-upper", Width: 1, Count: 4}, {Class: "ascii-digit", Width: 1, Count: 4}}}}, "FOREIGN": {Mode: api.SecretModeExposed}},
	}
}

func TestLocalProjectionValidatesFullUnionAndPreservesSeparateTrustedRoles(t *testing.T) {
	state := projectionUnionFixtureV1()
	before := api.CloneSessionSecretState(state)
	local, err := ProjectLocalSessionSecretStateV1(state, map[string]bool{"LOCAL": true})
	if err != nil || len(local.Primary) != 1 || len(local.Intent.Modes) != 1 || len(local.Intent.Binds) != 1 || !reflect.DeepEqual(local.Intent.TrustedUses, before.Intent.TrustedUses) || !reflect.DeepEqual(state, before) {
		t.Fatal("local projection widened a guest domain, dropped roles or mutated the original", err)
	}
	use := local.Intent.TrustedUses["JUDGE_ONLY"]
	use.Bind.Hosts[0] = "changed.example.test:443"
	if state.Intent.TrustedUses["JUDGE_ONLY"].Bind.Hosts[0] == use.Bind.Hosts[0] {
		t.Fatal("projection retained a mutable role binding alias")
	}
	if _, err := CloneLocalSessionSecretState(state, map[string]bool{"LOCAL": true}); err == nil {
		t.Fatal("the existing strict clone was relaxed for a full foreign union")
	}
}

func TestLocalProjectionCannotHideInvalidForeignPublicMetadata(t *testing.T) {
	state := projectionUnionFixtureV1()
	state.Intent.Modes["FOREIGN"] = api.SecretModeBound
	state.Intent.Binds["FOREIGN"] = state.Intent.Binds["LOCAL"]
	foreign := state.Primary["LOCAL"]
	foreign.Identity = strings.Repeat("c", 32)
	state.Primary["FOREIGN"] = foreign
	if local, err := ProjectLocalSessionSecretStateV1(state, map[string]bool{"LOCAL": true}); err == nil || local != nil {
		t.Fatal("projection discarded an invalid alias before full validation")
	}
}
