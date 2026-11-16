package egress

import (
	"net/http"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestKnownBoundHeaderNeverExemptsAnEmbeddedDecoy(t *testing.T) {
	decoyTestHome(t)
	generation := decoyTestGeneration(t)
	value := generation[0].Values[0]
	standin := api.Secret("prefix" + string(value) + "suffix")
	view, err := newRequestSecretSnapshot([]requestBoundSecret{{Name: "BOUND_KEY", Identity: strings.Repeat("a", 32), Binding: api.SecretBind{Hosts: []string{"example.com:443"}, Header: "X-Token", Format: "{value}"}, Sentinel: standin}})
	if err != nil {
		t.Fatal(err)
	}
	view, err = view.withDecoyTripwires(generation)
	if err != nil {
		t.Fatal(err)
	}
	request, original, checked := requestSecretFixture(t, "https", "example.com", "/probe", http.Header{"X-Token": {string(standin)}})
	failure := checkRequestSecretTripwires(view, request, original, checked, "https")
	if failure == nil || failure.status != http.StatusForbidden || failure.decoy != generation[0].Name {
		t.Fatal("a complete named-header stand-in exemption hid an embedded fake credential")
	}
}
