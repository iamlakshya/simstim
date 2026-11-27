package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestSessionSecretStateRetainsUnsupportedNestedAndFutureData(t *testing.T) {
	for _, raw := range []string{
		`{"version":2,"intent":{"version":1},"future":"keep"}`,
		`{"version":1,"intent":{"version":2},"primary":{}}`,
		`{"version":1,"intent":{"version":1,"modes":{},"binds":{},"future":true}}`,
		`{"version":1,"intent":{"version":1},"primary":{"KEY":{"mode":"bound","armed":false,"future":true}}}`,
		`{"version":1,"intent":{"version":1},"primary":{"KEY":{"shape":{"version":1,"slots":[],"future":true}}}}`,
		`{"version":2,"version":1,"intent":{"version":1}}`,
		`{"version":2,"Version":1,"intent":{"version":1}}`,
		`{"version":1,"intent":{"version":1,"Modes":{}}}`,
		`{"version":1,"intent":{"version":1,"modes":{"KEY":"bound","KEY":"exposed"}}}`,
		`{"version":1,"intent":{"version":1},"primary":"unsupported"}`,
	} {
		var state SessionSecretState
		if err := json.Unmarshal([]byte(raw), &state); err != nil || state.Supported() {
			t.Fatal("unsupported public state gained an interpretable legacy projection")
		}
		clone := CloneSessionSecretState(&state)
		encoded, err := json.Marshal(clone)
		if err != nil || !bytes.Equal(encoded, []byte(raw)) {
			t.Fatal("unsupported public state was lost while cloning or saving")
		}
		if strings.Contains(fmt.Sprintf("%#v", clone), "keep") {
			t.Fatal("opaque public state escaped diagnostic redaction")
		}
	}
}

func TestSessionSecretStateOwnsIntentPrimaryAndMintedShapes(t *testing.T) {
	state := &SessionSecretState{Version: SessionSecretStateV1, Intent: SecretDeliveryIntent{Version: SecretDeliveryIntentV1,
		Modes: map[string]SecretMode{"KEY": SecretModeBound}, Binds: map[string]SecretBind{"KEY": {Hosts: []string{"api.example.com:443"}, Header: "Authorization", Format: "Bearer {value}"}}},
		Primary: map[string]PublicSecretState{"KEY": {Mode: SecretModeBound, Sentinel: "public-primary", Shape: &SecretShape{Version: 1, Slots: []SecretShapeSlot{{Class: "lower", Count: 3}}}}},
		Minted:  map[string][]PublicMintedSecretState{"KEY": {{Sentinel: "public-minted", Shape: &SecretShape{Version: 1, Slots: []SecretShapeSlot{{Class: "upper", Count: 2}}}}}}}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var decoded SessionSecretState
	if json.Unmarshal(encoded, &decoded) != nil || !decoded.Supported() {
		t.Fatal("known public session state failed its shared structural envelope")
	}
	clone := CloneSessionSecretState(&decoded)
	clone.Intent.Modes["KEY"] = SecretModeExposed
	clone.Intent.Binds["KEY"].Hosts[0] = "changed"
	clone.Primary["KEY"].Shape.Slots[0].Count = 99
	clone.Minted["KEY"][0].Shape.Slots[0].Count = 99
	if decoded.Intent.Modes["KEY"] != SecretModeBound || decoded.Intent.Binds["KEY"].Hosts[0] == "changed" || decoded.Primary["KEY"].Shape.Slots[0].Count != 3 || decoded.Minted["KEY"][0].Shape.Slots[0].Count != 2 {
		t.Fatal("public state clone shared mutable binding or shape metadata")
	}
}
