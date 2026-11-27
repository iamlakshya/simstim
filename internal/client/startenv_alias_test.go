package client

import "testing"

func TestEnvironmentLocalLookupAliasIsCheckedBeforeLookup(t *testing.T) {
	inputs := envInputs{configured: map[string]bool{"API_KEY": true}, lookupFold: true,
		lookup: func(string) (string, bool) { t.Fatal("secret alias reached local lookup"); return "", false }}
	for _, name := range []string{"api_key", "ssh_auth_sock"} {
		if _, _, err := environmentEntry(name, inputs); err == nil {
			t.Fatalf("local alias %q accepted", name)
		}
	}
}

func TestEnvironmentLinuxKeysAndLiteralKeysRetainCase(t *testing.T) {
	inputs := envInputs{configured: map[string]bool{"API_KEY": true}, lookup: func(name string) (string, bool) { return name, true }}
	name, value, err := environmentEntry("api_key", inputs)
	if err != nil || name != "api_key" || value != "api_key" {
		t.Fatalf("case-sensitive lookup changed: %q %q %v", name, value, err)
	}
	inputs.lookupFold = true
	_, value, err = environmentEntry("api_key=ordinary literal", inputs)
	if err != nil || value != "ordinary literal" {
		t.Fatalf("literal performed a local lookup: %q %v", value, err)
	}
}
