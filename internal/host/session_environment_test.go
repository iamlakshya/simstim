package host

import (
	"reflect"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestCompleteEnvironmentRestoresFreshControlsAndRemovesOldOrdinaryKeys(t *testing.T) {
	base := map[string]string{"HOME": GuestHome, "PATH": guestPath("/opt/current/bin"), "TERM": "xterm-256color", "TERM_PROGRAM": ""}
	first, err := applyResolvedEnvironment(base, base, &api.SessionEnvironment{Version: 1, Values: map[string]string{"HOME": "/untrusted", "PATH": "/untrusted", "TERM_PROGRAM": "old-terminal", "ONLY_OLD": "old", "EMPTY": ""}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	values := environmentMap(first)
	if values["HOME"] != GuestHome || values["PATH"] != guestPath("/opt/current/bin") {
		t.Fatal("ordinary map replaced runtime controls")
	}
	if _, present := values["TERM_PROGRAM"]; present {
		t.Fatal("ordinary map invented absent current terminal metadata")
	}
	if value, present := values["EMPTY"]; !present || value != "" {
		t.Fatal("literal empty ordinary variable was discarded")
	}
	fresh, err := applyResolvedEnvironment(base, base, &api.SessionEnvironment{Version: 1, Values: map[string]string{}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := environmentMap(fresh)["ONLY_OLD"]; present {
		t.Fatal("known-empty used the previously merged environment as base")
	}
	if base["HOME"] != GuestHome || base["TERM_PROGRAM"] != "" {
		t.Fatal("runtime base was mutated")
	}
}

func TestResolvedEnvironmentRefusesSecretNamesBeforeExposingAResult(t *testing.T) {
	state := &api.SessionSecretState{Version: api.SessionSecretStateV1, Intent: api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: map[string]api.SecretMode{"CURRENT": api.SecretModeBound}}, Retired: map[string][]api.RetiredPrimarySecretState{"RETIRED": nil}}
	for _, name := range []string{"SSH_AUTH_SOCK", "CURRENT", "RETIRED", "LEGACY"} {
		result, err := applyResolvedEnvironment(map[string]string{"HOME": GuestHome}, nil, &api.SessionEnvironment{Version: 1, Values: map[string]string{name: "ordinary"}}, state, []string{"LEGACY"})
		if err == nil || result != nil {
			t.Fatal("ordinary map entered the protected secret namespace", name)
		}
	}
}

func TestResolvedEnvironmentEncodingIsStableAndKeepsLiteralEmpty(t *testing.T) {
	want := []string{"A=", "B=two", "Z=last"}
	for range 8 {
		if got := orderedEnvironment(map[string]string{"Z": "last", "A": "", "B": "two"}); !reflect.DeepEqual(got, want) {
			t.Fatal("environment encoding lost values or stable order", got)
		}
	}
}
