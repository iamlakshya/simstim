package egress

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func publicSessionFixture(t *testing.T) (InitArgs, *api.SessionSecretState, string, *Server) {
	t.Helper()
	args := boundRegistryArgs(map[string]string{"KEY": "ABCD0000"})
	server, result := newBoundRegistryServer(t, args)
	initial := args.Initial
	initial.Secrets = nil
	state := &api.SessionSecretState{Version: api.SessionSecretStateV1,
		Intent:     api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: args.Modes, Binds: args.Binds},
		Generation: result.Generation, Primary: result.Secrets}
	return initial, state, string(server.ca.publicPEM), server
}

func TestPublicSessionValidationIsPureAndUsesTheCanonicalSourceShapes(t *testing.T) {
	initial, state, ca, server := publicSessionFixture(t)
	before := api.CloneSessionSecretState(state)
	registry, engine, redaction := server.currentMembers(), server.currentEngine(), server.scrubber.snapshot()
	if err := ValidatePublicSessionState(initial, state, ca); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state, before) || registry != server.currentMembers() || engine != server.currentEngine() || redaction != server.scrubber.snapshot() {
		t.Fatal("public validation mutated caller state or published an engine/secret generation")
	}
	for _, mutate := range []func(*api.SessionSecretState){
		func(s *api.SessionSecretState) { s.Version++ },
		func(s *api.SessionSecretState) { s.Intent.Version++ },
		func(s *api.SessionSecretState) { s.Generation = "unknown" },
		func(s *api.SessionSecretState) { s.Intent.Modes["KEY"] = api.SecretModeExposed },
		func(s *api.SessionSecretState) { delete(s.Intent.Binds, "KEY") },
		func(s *api.SessionSecretState) { s.Primary["OTHER"] = s.Primary["KEY"] },
		func(s *api.SessionSecretState) {
			p := s.Primary["KEY"]
			p.Shape.Slots[0].ASCII = "observed"
			s.Primary["KEY"] = p
		},
		func(s *api.SessionSecretState) {
			s.Minted = map[string][]api.PublicMintedSecretState{"OTHER": {{Identity: strings.Repeat("a", 32)}}}
		},
	} {
		candidate := api.CloneSessionSecretState(state)
		mutate(candidate)
		if err := ValidatePublicSessionState(initial, candidate, ca); err == nil {
			t.Fatal("unsupported or inconsistent public intent/shape acquired a persistence ACK")
		}
	}
	initial.Secrets = map[string]string{"KEY": "must-never-be-persisted"}
	if err := ValidatePublicSessionState(initial, state, ca); err == nil {
		t.Fatal("a public-state sizing call accepted transient actual values")
	}
}

func TestPublicSessionUsesActualMaximumIDFrameBoundaryAndResultBytes(t *testing.T) {
	initial, state, ca, _ := publicSessionFixture(t)
	args := BoundInitArgs{Version: 2, Initial: initial, Modes: state.Intent.Modes, Binds: state.Intent.Binds, Saved: state.Primary}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	rawRequest, err := json.Marshal(proto.Request{ID: math.MaxUint64, Op: CtlBoundInit, Args: rawArgs})
	if err != nil {
		t.Fatal(err)
	}
	initial.SessionRef = strings.Repeat("p", proto.MaxFrame-1-len(rawRequest))
	if err := ValidatePublicSessionState(initial, state, ca); err != nil {
		t.Fatal("exactly representable public wire state hit a smaller ad hoc cap")
	}
	initial.SessionRef += "p"
	requireBoundError(t, ValidatePublicSessionState(initial, state, ca), "state_too_large")
	initial.SessionRef = ""
	// This intentionally malformed caller input isolates response sizing. The
	// trusted producer separately owns actual CA validity and uses ep.caCert.
	requireBoundError(t, ValidatePublicSessionState(initial, state, strings.Repeat("C", proto.MaxFrame)), "state_too_large")
}
