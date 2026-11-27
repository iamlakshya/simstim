package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

func validPublicMintFixture() (*api.SessionSecretState, egress.MintedStateEvent) {
	const owner = "CREDENTIAL"
	binding := api.SecretBind{Hosts: []string{"example.com:443"}, Header: "Authorization", Format: "Bearer {value}"}
	primary := api.PublicSecretState{Mode: api.SecretModeBound, Sentinel: api.Secret("STND0000"), Identity: strings.Repeat("a", 32), Armed: true,
		Shape: &api.SecretShape{Version: api.SecretShapeV1, Slots: []api.SecretShapeSlot{{Class: "ascii-upper", Count: 4, Width: 1}, {Class: "ascii-digit", Count: 4, Width: 1}}}}
	state := &api.SessionSecretState{Version: api.SessionSecretStateV1, Generation: strings.Repeat("c", 32),
		Intent:  api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: map[string]api.SecretMode{owner: api.SecretModeBound}, Binds: map[string]api.SecretBind{owner: binding}},
		Primary: map[string]api.PublicSecretState{owner: primary}}
	shape := &api.SecretShape{Version: api.SecretShapeV1, Rule: "openai-api-key"}
	for _, r := range "sk-" {
		shape.Slots = append(shape.Slots, api.SecretShapeSlot{Class: "ascii", ASCII: string(r), Count: 1, Width: 1})
	}
	const alnum = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	shape.Slots = append(shape.Slots, api.SecretShapeSlot{Class: "ascii", ASCII: alnum, Count: 20, Width: 1})
	for _, r := range "T3BlbkFJ" {
		shape.Slots = append(shape.Slots, api.SecretShapeSlot{Class: "ascii", ASCII: string(r), Count: 1, Width: 1})
	}
	shape.Slots = append(shape.Slots, api.SecretShapeSlot{Class: "ascii", ASCII: alnum, Count: 20, Width: 1})
	public := api.PublicMintedSecretState{Sentinel: api.Secret("sk-" + strings.Repeat("A", 20) + "T3BlbkFJ" + strings.Repeat("B", 20)),
		Shape: shape, Identity: strings.Repeat("b", 32), Authority: "example.com:443", Rule: shape.Rule}
	return state, egress.MintedStateEvent{Version: egress.MemberRegistryV2, Owner: owner, Generation: strings.Repeat("d", 32), Receipt: strings.Repeat("e", 32), State: public}
}

func TestMintCandidateUsesCanonicalPublicGrammarAndKeepsEpoch(t *testing.T) {
	state, event := validPublicMintFixture()
	next, err := mintCandidate(context.Background(), state, event)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Minted) != 0 || len(next.Minted[event.Owner]) != 1 || next.Generation != state.Generation || next.Primary[event.Owner].Sentinel != state.Primary[event.Owner].Sentinel {
		t.Fatal("candidate mutated current state, epoch or primary stand-in")
	}
	event.State.Shape.Slots[0].ASCII = "x"
	if next.Minted[event.Owner][0].Shape.Slots[0].ASCII != "s" {
		t.Fatal("candidate borrowed the received shape")
	}
}

func TestMintCandidateRetryIsExactAndIdempotent(t *testing.T) {
	state, event := validPublicMintFixture()
	first, err := mintCandidate(context.Background(), state, event)
	if err != nil {
		t.Fatal(err)
	}
	retry := event
	retry.Generation, retry.Receipt = strings.Repeat("f", 32), strings.Repeat("1", 32)
	next, err := mintCandidate(context.Background(), first, retry)
	if err != nil || len(next.Minted[event.Owner]) != 1 || next.Generation != first.Generation {
		t.Fatal("new receipt duplicated state or replaced the current global epoch")
	}
	conflict := event
	conflict.State.Sentinel = api.Secret("sk-" + strings.Repeat("C", 20) + "T3BlbkFJ" + strings.Repeat("D", 20))
	if _, err := mintCandidate(context.Background(), first, conflict); err == nil {
		t.Fatal("same identity changed its public stand-in")
	}
}

func TestMintCandidateCannotAddNameModeBindingOrUnsupportedState(t *testing.T) {
	cases := []struct {
		name string
		edit func(*api.SessionSecretState, *egress.MintedStateEvent)
	}{
		{"unknown owner", func(_ *api.SessionSecretState, e *egress.MintedStateEvent) { e.Owner = "UNKNOWN" }},
		{"exposed owner", func(s *api.SessionSecretState, _ *egress.MintedStateEvent) {
			s.Intent.Modes["CREDENTIAL"] = api.SecretModeExposed
		}},
		{"missing primary", func(s *api.SessionSecretState, _ *egress.MintedStateEvent) { delete(s.Primary, "CREDENTIAL") }},
		{"missing bind", func(s *api.SessionSecretState, _ *egress.MintedStateEvent) { delete(s.Intent.Binds, "CREDENTIAL") }},
		{"outside authority", func(_ *api.SessionSecretState, e *egress.MintedStateEvent) { e.State.Authority = "other.example:443" }},
		{"unknown rule", func(_ *api.SessionSecretState, e *egress.MintedStateEvent) { e.State.Rule = "unknown-rule" }},
		{"widened shape", func(_ *api.SessionSecretState, e *egress.MintedStateEvent) { e.State.Shape.Slots[0].ASCII = "st" }},
		{"future envelope", func(s *api.SessionSecretState, _ *egress.MintedStateEvent) { s.Version++ }},
		{"future event", func(_ *api.SessionSecretState, e *egress.MintedStateEvent) { e.Version++ }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, event := validPublicMintFixture()
			tc.edit(state, &event)
			before, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := mintCandidate(context.Background(), state, event); err == nil {
				t.Fatal("minted metadata conferred authority or bypassed canonical validation")
			}
			after, err := json.Marshal(state)
			if err != nil || string(before) != string(after) {
				t.Fatal("refused proposal changed current public metadata")
			}
		})
	}
}

func TestMintCandidateCancellationAndErrorsCarryNoPublicValues(t *testing.T) {
	state, event := validPublicMintFixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := mintCandidate(ctx, state, event); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled proposal was accepted")
	}
	event.State.Authority = "unapproved.example:443"
	_, err := mintCandidate(context.Background(), state, event)
	if err == nil || strings.Contains(err.Error(), string(event.State.Sentinel)) || strings.Contains(err.Error(), event.State.Authority) || strings.Contains(err.Error(), event.Receipt) {
		t.Fatal("proposal was accepted or diagnostic echoed public state/receipt")
	}
}
