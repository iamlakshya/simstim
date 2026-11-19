package egress

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func freshValuesCohortFixture(t *testing.T) *api.SessionSecretState {
	t.Helper()
	primary := retiredPrimaryFixture("AAAA0000", 'a')
	retired := retiredPrimaryFixture("BBBB1111", 'b')
	value := api.Secret("sk-" + strings.Repeat("C", 20) + "T3BlbkFJ" + strings.Repeat("D", 20))
	shape, rule, err := compileBoundShape(t.Context(), "COHORT", value, "", bindingPunctuationPolicy(primary.Binding))
	if err != nil {
		t.Fatal(err)
	}
	public, err := publicSecretShape(t.Context(), "COHORT", shape, rule)
	if err != nil {
		t.Fatal(err)
	}
	state := &api.SessionSecretState{Version: api.SessionSecretStateV1, Generation: strings.Repeat("c", 32), Intent: api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: map[string]api.SecretMode{"COHORT": api.SecretModeBound}, Binds: map[string]api.SecretBind{"COHORT": primary.Binding}}, Primary: map[string]api.PublicSecretState{"COHORT": {Mode: api.SecretModeBound, Sentinel: primary.Sentinel, Shape: primary.Shape, Identity: primary.Identity}}, Minted: map[string][]api.PublicMintedSecretState{"COHORT": {{Sentinel: value, Shape: public, Rule: rule, Identity: strings.Repeat("d", 32), Authority: "example.com:443"}}}, Retired: map[string][]api.RetiredPrimarySecretState{"OLD_COHORT": {retired}}}
	if err := ValidateSavedPublicSessionState(state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestFreshValuesCheckAllCohortStandInsWithoutRequiringUnrelatedValues(t *testing.T) {
	state := freshValuesCohortFixture(t)
	before := api.CloneSessionSecretState(state)
	values := map[string]api.Secret{"CHILD": "fresh-child-value"}
	original := map[string]api.Secret{"CHILD": "fresh-child-value"}
	if err := ValidateFreshValuesAgainstPublicState(t.Context(), state, values); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFreshValuesAgainstPublicState(t.Context(), state, nil); err != nil {
		t.Fatal("unrelated cohort values became required")
	}
	if !reflect.DeepEqual(state, before) || !reflect.DeepEqual(values, original) {
		t.Fatal("pure collision validation mutated caller data")
	}
	for _, sentinel := range []api.Secret{state.Primary["COHORT"].Sentinel, state.Minted["COHORT"][0].Sentinel, state.Retired["OLD_COHORT"][0].Sentinel} {
		err := ValidateFreshValuesAgainstPublicState(t.Context(), state, map[string]api.Secret{"CHILD": sentinel})
		var public *proto.Error
		if err == nil || !errors.As(err, &public) || public.Hint == "" || strings.Contains(public.Error()+public.Hint, string(sentinel)) {
			t.Fatal("fresh child value equalled an unrelated stand-in or leaked its value")
		}
	}
}

func TestFreshValuesValidateWholeSavedStateAndActualPairsBeforeSuccess(t *testing.T) {
	state := freshValuesCohortFixture(t)
	for _, mutate := range []func(*api.SessionSecretState){func(s *api.SessionSecretState) { s.Version++ }, func(s *api.SessionSecretState) { s.Generation = "invalid" }, func(s *api.SessionSecretState) {
		p := s.Primary["COHORT"]
		p.Identity = "invalid"
		s.Primary["COHORT"] = p
	}, func(s *api.SessionSecretState) { s.Minted["COHORT"][0].Authority = "other.example:443" }, func(s *api.SessionSecretState) { s.Retired["OLD_COHORT"][0].Shape = nil }} {
		candidate := api.CloneSessionSecretState(state)
		mutate(candidate)
		if err := ValidateFreshValuesAgainstPublicState(t.Context(), candidate, map[string]api.Secret{"CHILD": "fresh"}); err == nil {
			t.Fatal("unrelated invalid/future cohort metadata was ignored")
		}
	}
	for _, values := range []map[string]api.Secret{{"bad-name": "fresh"}, {"TERM": "fresh"}, {"CHILD": "nul\x00value"}} {
		if err := ValidateFreshValuesAgainstPublicState(t.Context(), state, values); err == nil {
			t.Fatal("invalid supplied actual name/value pair was accepted")
		}
	}
	if err := ValidateFreshValuesAgainstPublicState(t.Context(), nil, nil); err == nil {
		t.Fatal("absent complete saved state was accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := ValidateFreshValuesAgainstPublicState(ctx, state, map[string]api.Secret{"CHILD": "fresh"}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled fresh-value check succeeded")
	}
}
