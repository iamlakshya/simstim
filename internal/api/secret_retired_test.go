package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestRetiredPublicStateIsOwnedAndHasNoActiveCredentialFields(t *testing.T) {
	state := &SessionSecretState{Version: SessionSecretStateV1, Intent: SecretDeliveryIntent{Version: SecretDeliveryIntentV1},
		Retired: map[string][]RetiredPrimarySecretState{"KEY": {{Sentinel: "older-public", Identity: "retired-identity",
			Shape: &SecretShape{Version: 1, Slots: []SecretShapeSlot{{Class: "lower", Count: 5}}}, Binding: SecretBind{Hosts: []string{"api.example.com:443"}, Header: "Authorization", Format: "Bearer {value}"}}}}}
	clone := CloneSessionSecretState(state)
	clone.Retired["KEY"][0].Shape.Slots[0].Count = 99
	clone.Retired["KEY"][0].Binding.Hosts[0] = "changed"
	if state.Retired["KEY"][0].Shape.Slots[0].Count != 5 || state.Retired["KEY"][0].Binding.Hosts[0] == "changed" {
		t.Fatal("retired public metadata shared mutable shape or historical binding storage")
	}
	encoded, err := json.Marshal(state)
	if err != nil || strings.Contains(string(encoded), `"armed"`) || strings.Contains(string(encoded), `"value"`) || strings.Contains(string(encoded), `"rule"`) {
		t.Fatal("retired public record acquired active or minted credential fields")
	}
	var decoded SessionSecretState
	if json.Unmarshal(encoded, &decoded) != nil || !decoded.Supported() || len(decoded.Retired["KEY"]) != 1 {
		t.Fatal("known retired metadata failed its sole public envelope")
	}
	if strings.Contains(fmt.Sprintf("%#v", state.Retired["KEY"][0]), "older-public") {
		t.Fatal("retired public sentinel escaped diagnostic redaction")
	}
}
