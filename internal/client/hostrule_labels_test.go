package client

import (
	"errors"
	"reflect"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestHostRuleLabelsCannotChangeOutboundDecisions(t *testing.T) {
	settings := DefaultSettings()
	settings.Secrets = map[string]SecretDefinition{"TOKEN": {Run: "fixture-secret-reader", OnHosts: []string{"box"}}}
	for _, tier := range []Tier{TierOwn, TierThrowaway, TierShared} {
		for _, outbound := range []Outbound{{Kind: OutboundSecret, Name: "TOKEN"}, {Kind: OutboundDefaultHost}, {Kind: OutboundPool}} {
			plain := Host{Name: "box"}
			labelled := plain
			labelled.Labels = map[string]string{"tier": "own", "pool": "true", "secret": "TOKEN", "location": "preferred"}
			before, after := Allow(plain, tier, outbound, settings), Allow(labelled, tier, outbound, settings)
			if (before == nil) != (after == nil) {
				t.Fatal("label data changed an outbound decision", tier, outbound.Kind, before, after)
			}
			var first, second *UserError
			if errors.As(before, &first) && (!errors.As(after, &second) || first.Code != second.Code || first.Kind != second.Kind) {
				t.Fatal("labels changed the grant refusal", before, after)
			}
		}
	}
}

func TestHostRuleLabelsCannotChangeAutoPoolOrRank(t *testing.T) {
	makeCandidates := func(labels bool) []*autoPlacementCandidate {
		var result []*autoPlacementCandidate
		for _, name := range []string{"other", "box"} {
			h := Host{Name: name}
			if labels {
				h.Labels = map[string]string{"tier": "own", "pool": "true", "gpu": "true", "cost": "free"}
			}
			result = append(result, &autoPlacementCandidate{host: h, observation: &hostFactsObservation{facts: api.HostFacts{}}, room: 4})
		}
		return result
	}
	plain, labelled := makeCandidates(false), makeCandidates(true)
	for _, tier := range []Tier{TierOwn, TierThrowaway, TierShared} {
		before, err1 := poolPlacementReason(plain[0].host, tier, PlacementNeed{})
		after, err2 := poolPlacementReason(labelled[0].host, tier, PlacementNeed{})
		if before != after || (err1 == nil) != (err2 == nil) {
			t.Fatal("labels changed automatic-pool eligibility", before, after, err1, err2)
		}
	}
	rankPlacementCandidates(plain, PlacementSettings{Strategy: "spread"}, nil)
	rankPlacementCandidates(labelled, PlacementSettings{Strategy: "spread"}, nil)
	if plain[0].host.Name != labelled[0].host.Name || plain[1].host.Name != labelled[1].host.Name {
		t.Fatal("labels changed automatic placement order")
	}
}

func TestHostRuleLabelsDoNotChangeCoherentFactsFiltering(t *testing.T) {
	var outcomes []struct {
		reason string
		level  string
		room   int64
	}
	for _, test := range []struct {
		name   string
		labels map[string]string
	}{{"unlabelled", nil}, {"labelled", map[string]string{"tier": "own", "pool": "true", "gpu": "true", "cost": "free"}}} {
		t.Run(test.name, func(t *testing.T) {
			// Each candidate is captured from its own actual local saved entry.
			// This does not weaken the Source guard by editing an already captured
			// connection or observation after its membership snapshot.
			candidate := placementCandidateFixture(t, func(host *Host) { host.Labels = test.labels }, nil)
			reason, err := filterPlacementFacts(t.Context(), candidate, 0, false, true)
			if err != nil {
				t.Fatal(err)
			}
			outcomes = append(outcomes, struct {
				reason string
				level  string
				room   int64
			}{reason, candidate.level, candidate.room})
		})
	}
	if len(outcomes) != 2 {
		t.Fatal("a coherent facts fixture did not finish")
	}
	if !reflect.DeepEqual(outcomes[0], outcomes[1]) {
		t.Fatal("labels changed the actual facts filter", outcomes)
	}
}
