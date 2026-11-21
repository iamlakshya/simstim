package api

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func readinessCohortFixtureV1() *SandboxCohortView {
	return &SandboxCohortView{
		Snapshot:      &SandboxSnapshot{Version: 1, Primary: "a2a2a2a2", Generation: strings.Repeat("b", 64), Members: []string{}},
		SecretState:   &SessionSecretState{Version: 1, Generation: strings.Repeat("c", 32), Intent: SecretDeliveryIntent{Version: 1, Modes: map[string]SecretMode{}, Binds: map[string]SecretBind{}}},
		RunGeneration: strings.Repeat("d", 32),
	}
}

func TestProtectedCohortReadinessOwnsTransientNegativeData(t *testing.T) {
	view := readinessCohortFixtureV1()
	// A newly configured key may be absent from historical public metadata.
	// The actual original caller still validates this against its own query.
	view.TrustedReadiness = &TrustedSecretReadinessV1{Version: 1, Generation: view.SecretState.Generation, Unarmed: []string{"NEW_KEY"}}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSandboxCohort(raw, view.Snapshot.Primary)
	if err != nil || decoded.TrustedReadiness == nil || len(decoded.SecretState.Intent.TrustedUses) != 0 {
		t.Fatal("a negative observation was lost or became a registered role", err)
	}
	clone := decoded.Clone()
	clone.TrustedReadiness.Unarmed[0] = "CHANGED"
	if decoded.TrustedReadiness.Unarmed[0] != "NEW_KEY" {
		t.Fatal("the transient observation borrowed mutable data")
	}
	if decoded.TrustedReadiness.ValidFor(TrustedSecretReadinessQueryV1{Version: 1, Generation: decoded.SecretState.Generation, Uses: map[string]TrustedSecretUseV1{"OTHER": {}}}) {
		t.Fatal("a descriptive negative result matched another original query")
	}
}

func TestProtectedCohortReadinessRejectsAmbiguityAndUnknownEnvelope(t *testing.T) {
	view := readinessCohortFixtureV1()
	raw, _ := json.Marshal(view)
	for _, body := range []string{
		`{"v":1,"generation":"` + view.SecretState.Generation + `","unarmed":null}`,
		`{"v":1,"generation":"` + view.SecretState.Generation + `","unarmed":["KEY","KEY"]}`,
		`{"v":1,"generation":"` + view.SecretState.Generation + `","unarmed":["Z","A"]}`,
		`{"v":1,"generation":"other","unarmed":[]}`,
		`{"v":1,"V":1,"generation":"` + view.SecretState.Generation + `","unarmed":[]}`,
		`{"v":1,"generation":"` + view.SecretState.Generation + `","unarmed":[],"value":"forbidden"}`,
	} {
		encoded := append(bytes.TrimSuffix(raw, []byte("}")), []byte(`,"trusted_readiness":`+body+`}`)...)
		if _, err := DecodeSandboxCohort(encoded, view.Snapshot.Primary); err == nil {
			t.Fatalf("ambiguous or unsupported live observation accepted: %s", body)
		}
	}
	unknown := append(bytes.TrimSuffix(raw, []byte("}")), []byte(`,"future":true}`)...)
	if _, err := DecodeSandboxCohort(unknown, view.Snapshot.Primary); err == nil {
		t.Fatal("the optional observation widened the old outer envelope")
	}
}
