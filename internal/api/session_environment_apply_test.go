package api

import "testing"

func TestCompleteOrdinaryEnvironmentRebuildClearsOverridesAndPreservesEmpty(t *testing.T) {
	base := map[string]string{"HOME": "fixed-home", "PATH": "fixed-path"}
	first, err := ApplySessionEnvironment(base, &SessionEnvironment{Version: 1, Values: map[string]string{"HOME": "override", "ONLY_OLD": "old", "EMPTY": ""}}, nil, nil)
	if err != nil || first["HOME"] != "override" {
		t.Fatal(err)
	}
	fresh, err := ApplySessionEnvironment(base, &SessionEnvironment{Version: 1, Values: map[string]string{}}, nil, nil)
	if err != nil || fresh["HOME"] != "fixed-home" || fresh["PATH"] != "fixed-path" {
		t.Fatal("known-empty did not restore fresh runtime base")
	}
	if _, found := fresh["ONLY_OLD"]; found {
		t.Fatal("old CLI ordinary override survived complete replacement")
	}
	if value, found := first["EMPTY"]; !found || value != "" {
		t.Fatal("literal empty became unset")
	}
	if base["HOME"] != "fixed-home" {
		t.Fatal("runtime base was mutated")
	}
}

func TestOrdinaryEnvironmentRefusesUnknownAndCurrentRetiredSecretNames(t *testing.T) {
	state := &SessionSecretState{Version: SessionSecretStateV1, Intent: SecretDeliveryIntent{Version: SecretDeliveryIntentV1}, Retired: map[string][]RetiredPrimarySecretState{"RETIRED": nil}}
	for _, environment := range []*SessionEnvironment{nil, {Version: 2, Values: map[string]string{}}, {Version: 1}, {Version: 1, Values: map[string]string{"BAD-NAME": "x"}}, {Version: 1, Values: map[string]string{"SSH_AUTH_SOCK": "x"}}, {Version: 1, Values: map[string]string{"RETIRED": "x"}}, {Version: 1, Values: map[string]string{"LEGACY": "x"}}} {
		if _, err := ApplySessionEnvironment(nil, environment, state, []string{"LEGACY"}); err == nil {
			t.Fatal("unknown or protected ordinary map accepted")
		}
	}
}
