package supervisor

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestColdBoundEnvironmentKeepsStandInAndMissingWarning(t *testing.T) {
	state, _ := validPublicMintFixture()
	primary := state.Primary["CREDENTIAL"]
	primary.Armed = false
	state.Primary["CREDENTIAL"] = primary
	env, err := secretEnvironmentFor([]string{"CREDENTIAL"}, state.Intent, state.Primary, nil)
	if err != nil || env.Env["CREDENTIAL"] != string(primary.Sentinel) || env.Modes["CREDENTIAL"] != api.SecretModeBound || len(env.Missing) != 1 || env.Missing[0] != "CREDENTIAL" {
		t.Fatal("cold sentinel masked missing actual value or was omitted")
	}
}

func TestExposedMissingAndExplicitEmptyAreDistinct(t *testing.T) {
	intent := api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: map[string]api.SecretMode{"EXPOSED": api.SecretModeExposed}}
	public := map[string]api.PublicSecretState{"EXPOSED": {Mode: api.SecretModeExposed}}
	missing, err := secretEnvironmentFor([]string{"EXPOSED"}, intent, public, nil)
	if err != nil || len(missing.Env) != 0 || len(missing.Modes) != 0 || missing.Modes == nil || len(missing.Missing) != 1 {
		t.Fatal("missing exposure became an empty value or legacy mode")
	}
	explicit, err := secretEnvironmentFor([]string{"EXPOSED"}, intent, public, map[string]string{"EXPOSED": ""})
	value, present := explicit.Env["EXPOSED"]
	if err != nil || !present || value != "" || explicit.Modes["EXPOSED"] != api.SecretModeExposed || len(explicit.Missing) != 0 {
		t.Fatal("explicit approved empty exposure was discarded")
	}
}

func TestSecretEnvironmentSelectsOnlyThisProcessAndHidesDiagnostics(t *testing.T) {
	state, _ := validPublicMintFixture()
	state.Intent.Modes["OTHER"] = api.SecretModeExposed
	state.Primary["OTHER"] = api.PublicSecretState{Mode: api.SecretModeExposed}
	const value = "private-value-marker"
	env, err := secretEnvironmentFor([]string{"CREDENTIAL"}, state.Intent, state.Primary, map[string]string{"CREDENTIAL": value, "OTHER": "other-private-marker"})
	if err != nil || len(env.Env) != 1 || env.Env["CREDENTIAL"] == value || len(env.Modes) != len(env.Env) {
		t.Fatal("safe environment leaked or included another process's values")
	}
	for _, verb := range []string{"%v", "%+v", "%#v"} {
		if fmt.Sprintf(verb, env) != api.Redacted {
			t.Fatal("environment result formatting exposed values")
		}
	}
}

func TestSecretEnvironmentRefusesUnknownOrRawBoundReply(t *testing.T) {
	state, _ := validPublicMintFixture()
	value := string(state.Primary["CREDENTIAL"].Sentinel)
	if _, err := secretEnvironmentFor([]string{"CREDENTIAL"}, state.Intent, state.Primary, map[string]string{"CREDENTIAL": value}); err == nil || strings.Contains(err.Error(), value) {
		t.Fatal("bound reply echoed an actual value or error echoed it")
	}
	state.Intent.Version++
	if _, err := secretEnvironmentFor([]string{"CREDENTIAL"}, state.Intent, state.Primary, nil); err == nil {
		t.Fatal("future intent became legacy exposure")
	}
}

func TestUnsupportedSavedSecretStateRemainsOpaqueAndRefuses(t *testing.T) {
	raw := []byte(`{"version":99,"future":"public-opaque-marker"}`)
	var state api.SessionSecretState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if err := knownSessionSecretState(&state); err == nil {
		t.Fatal("unknown saved state was treated as supported legacy state")
	}
	after, err := json.Marshal(state)
	if err != nil || string(after) != string(raw) {
		t.Fatal("refusal rewrote opaque future state")
	}
	if err := knownSessionSecretState(nil); err != nil {
		t.Fatal("truly legacy absent state lost compatibility")
	}
}

func TestBoundInputReferencesAreDroppedWithoutRewritingExposures(t *testing.T) {
	values := map[string]string{"BOUND": "private-bound-marker", "EXPOSED": "private-exposed-marker"}
	dropBoundValues(values, map[string]api.SecretMode{"BOUND": api.SecretModeBound, "EXPOSED": api.SecretModeExposed})
	if _, retained := values["BOUND"]; retained || values["EXPOSED"] != "private-exposed-marker" {
		t.Fatal("bound reference retained or explicit exposure rewritten")
	}
}
