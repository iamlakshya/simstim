package egress

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func localPublicStateFixture() *api.SessionSecretState {
	binding := api.SecretBind{Hosts: []string{"example.com:443"}, Header: "Authorization", Format: "Bearer {value}"}
	shape := &api.SecretShape{Version: 1, Slots: []api.SecretShapeSlot{{Class: "ascii-upper", Count: 4, Width: 1}, {Class: "ascii-digit", Count: 4, Width: 1}}}
	return &api.SessionSecretState{Version: api.SessionSecretStateV1, Generation: strings.Repeat("c", 32),
		Intent:  api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: map[string]api.SecretMode{"KEY": api.SecretModeBound}, Binds: map[string]api.SecretBind{"KEY": binding}},
		Primary: map[string]api.PublicSecretState{"KEY": {Mode: api.SecretModeBound, Sentinel: "STND0000", Shape: shape, Identity: strings.Repeat("a", 32)}},
		Retired: map[string][]api.RetiredPrimarySecretState{"KEY": {{Sentinel: "OLDR0000", Shape: shape, Identity: strings.Repeat("b", 32), Binding: binding}}}}
}

func TestLocalPublicStateCloneKeepsCanonicalValidationAndOwnsAllMetadata(t *testing.T) {
	state := localPublicStateFixture()
	owned, err := CloneLocalSessionSecretState(state, map[string]bool{"KEY": true})
	if err != nil || owned == nil || owned.Generation != state.Generation {
		t.Fatal("canonical selected local state was refused")
	}
	owned.Intent.Binds["KEY"].Hosts[0] = "changed.example:443"
	owned.Primary["KEY"].Shape.Slots[0].Count = 99
	owned.Retired["KEY"][0].Binding.Hosts[0] = "retired-change.example:443"
	if state.Intent.Binds["KEY"].Hosts[0] != "example.com:443" || state.Primary["KEY"].Shape.Slots[0].Count != 4 || state.Retired["KEY"][0].Binding.Hosts[0] != "example.com:443" {
		t.Fatal("local receipt clone aliases its caller's public metadata")
	}
	state.Primary["KEY"] = api.PublicSecretState{Mode: api.SecretModeBound, Sentinel: "invalid"}
	if _, err := CloneLocalSessionSecretState(state, map[string]bool{"KEY": true}); err == nil {
		t.Fatal("local name membership bypassed canonical shape/identity validation")
	}
}

func TestLocalPublicStateDomainIncludesHistoricalRetiredAndEveryMap(t *testing.T) {
	for _, change := range []func(*api.SessionSecretState){
		func(s *api.SessionSecretState) { s.Intent.Modes["FOREIGN"] = api.SecretModeExposed },
		func(s *api.SessionSecretState) { s.Intent.Binds["FOREIGN"] = s.Intent.Binds["KEY"] },
		func(s *api.SessionSecretState) {
			s.Primary["FOREIGN"] = api.PublicSecretState{Mode: api.SecretModeExposed}
		},
		func(s *api.SessionSecretState) { s.Minted = map[string][]api.PublicMintedSecretState{"FOREIGN": {}} },
		func(s *api.SessionSecretState) { s.Retired["FOREIGN"] = s.Retired["KEY"] },
	} {
		state := localPublicStateFixture()
		change(state)
		if owned, err := CloneLocalSessionSecretState(state, map[string]bool{"KEY": true}); err == nil || owned != nil {
			t.Fatal("a current or historical foreign name entered the local receipt domain")
		}
	}
	state := localPublicStateFixture()
	state.Retired["FOREIGN"] = state.Retired["KEY"]
	delete(state.Retired, "KEY")
	if err := ValidateSavedPublicSessionState(state); err != nil {
		t.Fatal("retired-only foreign-name witness is not otherwise canonical")
	}
	if _, err := CloneLocalSessionSecretState(state, map[string]bool{"KEY": true, "FOREIGN": false}); err == nil {
		t.Fatal("an explicit false local domain entry gained permission")
	}
}
