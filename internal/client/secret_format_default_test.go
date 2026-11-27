package client

import (
	"encoding/json"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestCustomBindingHostsAndHeaderNormaliseToExplicitRawFormat(t *testing.T) {
	view, err := NormaliseSecretDefinitions(map[string]SecretDefinition{"CUSTOM": {Run: "printf fixture", Hosts: []string{"API.example.com"}, Header: "X-Credential"}}, nil)
	if err != nil || view.Binds["CUSTOM"].Format != "{value}" || view.Binds["CUSTOM"].Hosts[0] != "api.example.com:443" {
		t.Fatal("hosts plus header could not produce the specified raw binding default")
	}
	intent := api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: map[string]api.SecretMode{"CUSTOM": api.SecretModeBound}, Binds: view.Binds}
	encoded, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	var restored api.SecretDeliveryIntent
	if json.Unmarshal(encoded, &restored) != nil || restored.Binds["CUSTOM"].Format != "{value}" || restored.Binds["CUSTOM"].Header != "X-Credential" {
		t.Fatal("normalised binding did not retain explicit restart metadata")
	}
}

func TestBindingFormatDefaultNeverInfersHeaderOrReplacesValidFormats(t *testing.T) {
	for _, format := range []string{"Bearer {value}", "basic:fixed:{value}"} {
		for _, explicit := range []bool{false, true} {
			definition := SecretDefinition{Run: "printf fixture", Hosts: []string{"api.example.com"}, Header: "Authorization"}
			var defaults map[string]api.SecretBind
			if explicit {
				definition.Format = format
			} else {
				defaults = map[string]api.SecretBind{"CUSTOM": {Hosts: []string{"api.example.com"}, Header: "Authorization", Format: format}}
			}
			view, err := NormaliseSecretDefinitions(map[string]SecretDefinition{"CUSTOM": definition}, defaults)
			if err != nil || view.Binds["CUSTOM"].Format != format {
				t.Fatal("raw default replaced an explicit or selected valid credential format")
			}
		}
	}
	if _, err := NormaliseSecretDefinitions(map[string]SecretDefinition{"CUSTOM": {Run: "printf fixture", Hosts: []string{"api.example.com"}}}, nil); err == nil {
		t.Fatal("a missing unverified header was inferred from hosts alone")
	}
	definition, err := DecodeSecretDefinition(map[string]any{"run": "printf fixture", "hosts": []string{"api.example.com"}, "header": "X-Credential", "format": ""})
	if err != nil {
		t.Fatal("structural decoder changed its transactional semantic boundary")
	}
	if _, err := NormaliseSecretDefinitions(map[string]SecretDefinition{"CUSTOM": definition}, nil); err == nil {
		t.Fatal("an explicitly empty format was silently treated as absent")
	}
}
