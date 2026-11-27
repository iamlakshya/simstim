package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSecretSummaryProjectsOnlyAppliedMemberNamesAndOwnsItsMap(t *testing.T) {
	state := &SessionSecretState{Version: SessionSecretStateV1, Generation: strings.Repeat("a", 32),
		Intent:  SecretDeliveryIntent{Version: SecretDeliveryIntentV1, Modes: map[string]SecretMode{"OWN": SecretModeBound, "OTHER": SecretModeExposed}},
		Primary: map[string]PublicSecretState{"OWN": {Mode: SecretModeBound, Armed: true}, "OTHER": {Mode: SecretModeExposed}}}
	summary := SummariseSessionSecrets(state, []string{"OWN"})
	if !summary.Supported() || len(summary.Secrets) != 1 || summary.Secrets["OWN"].Mode != SecretModeBound || !summary.Secrets["OWN"].Armed || summary.Generation != state.Generation {
		t.Fatal("compact member view lost its applied mode, arming or current CAS")
	}
	clone := CloneSessionSecretSummary(summary)
	delete(clone.Secrets, "OWN")
	state.Primary["OWN"] = PublicSecretState{Mode: SecretModeBound}
	if len(summary.Secrets) != 1 || !summary.Secrets["OWN"].Armed {
		t.Fatal("summary map or status aliases mutable input")
	}
}

func TestSecretSummaryUnknownNeverFallsBackToExposure(t *testing.T) {
	if SummariseSessionSecrets(nil, []string{"KEY"}) != nil || (*SessionSecretSummary)(nil).Supported() {
		t.Fatal("absent state acquired a delivery interpretation")
	}
	for _, raw := range []string{
		`{"version":2,"generation":"future","secrets":{"KEY":{"mode":"exposed","armed":true}}}`,
		`{"version":1,"generation":"g","secrets":{"KEY":{"mode":"future","armed":true}}}`,
		`{"version":1,"Version":1,"secrets":{"KEY":{"mode":"exposed","armed":true}}}`,
		`{"version":1,"version":1,"secrets":{"KEY":{"mode":"exposed","armed":true}}}`,
		`{"version":1,"secrets":{"KEY":{"mode":"exposed","armed":true,"sentinel":"future"}}}`,
		`{"version":1,"unsupported":true,"secrets":{"KEY":{"mode":"exposed","armed":true}}}`,
	} {
		var summary SessionSecretSummary
		if err := json.Unmarshal([]byte(raw), &summary); err != nil || summary.Supported() || !summary.Unsupported || len(summary.Secrets) != 0 || summary.Generation != "" {
			t.Fatal("unsupported compact metadata retained a usable mode, name or CAS")
		}
	}
}

func TestSecretSummaryCarriesNoPublicStandInBindingShapeOrHistory(t *testing.T) {
	state := &SessionSecretState{Version: SessionSecretStateV1, Generation: strings.Repeat("a", 32),
		Intent: SecretDeliveryIntent{Version: SecretDeliveryIntentV1, Modes: map[string]SecretMode{"KEY": SecretModeBound},
			Binds: map[string]SecretBind{"KEY": {Hosts: []string{"private.example:443"}, Header: "Authorization", Format: "Bearer {value}"}}},
		Primary: map[string]PublicSecretState{"KEY": {Mode: SecretModeBound, Sentinel: "public-current", Identity: "private-identity", Shape: &SecretShape{Version: 1}}},
		Minted:  map[string][]PublicMintedSecretState{"KEY": {{Sentinel: "public-minted"}}},
		Retired: map[string][]RetiredPrimarySecretState{"KEY": {{Sentinel: "public-retired"}}}}
	raw, err := json.Marshal(SummariseSessionSecrets(state, []string{"KEY"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"public-current", "public-minted", "public-retired", "private.example", "private-identity", "shape", "binding", "Authorization"} {
		if strings.Contains(string(raw), marker) {
			t.Fatal("ordinary compact metadata retained full publication data")
		}
	}
	var roundTrip SessionSecretSummary
	if json.Unmarshal(raw, &roundTrip) != nil || !roundTrip.Supported() || roundTrip.Secrets["KEY"].Mode != SecretModeBound {
		t.Fatal("canonical compact view did not survive the actual JSON codec")
	}
}
