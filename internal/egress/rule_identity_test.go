package egress

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func fixedCollisionRules(layer api.LayerKind, name, first, second string) []api.Rule {
	rules := []api.Rule{{Layer: layer, Name: name, Allow: true, Door: api.DoorNet, Target: first}, {Layer: layer, Name: name, Allow: true, Door: api.DoorNet, Target: second}}
	for i := range rules {
		rules[i].ID = api.RuleID(rules[i])
	}
	return rules
}

func requireRuleAmbiguity(t *testing.T, err error) {
	t.Helper()
	var problem *proto.Error
	if !errors.As(err, &problem) || problem.Code != "rule_id_ambiguous" || problem.Hint == "" {
		t.Fatal("different semantic rules sharing a real public ID were not refused with a hinted ambiguity error")
	}
}

func TestRealRuleShortCollisionRefusesMergeAndDirectEngineBeforePermits(t *testing.T) {
	rules := fixedCollisionRules(api.LayerAll, "", "collision-6295.example", "collision-6343.example")
	if rules[0].ID != "gcjvwfw" || rules[1].ID != "gcjvwfw" {
		t.Fatal("fixed real SHA-256 collision fixture changed")
	}
	before := api.CloneLayers([]api.Layer{{Kind: api.LayerAll, Rules: rules}})
	for _, input := range [][]api.Rule{rules, {rules[1], rules[0]}} {
		pol, cleaned, err := MergeLayers([]api.Layer{{Kind: api.LayerAll, Rules: input}})
		requireRuleAmbiguity(t, err)
		if len(pol.Rules) != 0 || cleaned != nil {
			t.Fatal("a colliding merge published partial effective rules")
		}
		for _, order := range [][]api.LayerRef{nil, {{Kind: api.LayerAll}}} {
			engine, err := NewEngine("", api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownDeny, Rules: input, Order: order})
			requireRuleAmbiguity(t, err)
			if engine != nil {
				t.Fatal("colliding direct policy exposed an engine or once ledger")
			}
		}
	}
	if !reflect.DeepEqual(before[0].Rules, rules) {
		t.Fatal("collision refusal mutated source rules")
	}
	for _, rule := range rules {
		engine, err := NewEngine("", api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownDeny, Rules: []api.Rule{rule}})
		if err != nil {
			t.Fatal(err)
		}
		if !engine.Decide(Request{Host: rule.Target, Port: 443}).Allow {
			t.Fatal("a valid single rule no longer authorises its own distinct target")
		}
	}
}

func TestRuleIdentityDeduplicatesOnlyEqualInstancesAndKeepsDeterministicLeases(t *testing.T) {
	deadline := time.Date(2500, time.March, 2, 3, 4, 5, 123456789, time.UTC)
	first := api.Rule{Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: "lease.example", Once: true, Until: deadline, GrantID: strings.Repeat("1", 32)}
	first.ID = api.RuleID(first)
	equal := first
	equal.Until = deadline.In(time.FixedZone("offset", 3600))
	pol, _, err := MergeLayers([]api.Layer{{Kind: api.LayerSession, Rules: []api.Rule{first, equal}}})
	if err != nil || len(pol.Rules) != 1 {
		t.Fatal("equal complete rule instances did not count once")
	}
	second := first
	second.GrantID = strings.Repeat("2", 32)
	var previous []api.Rule
	for _, rules := range [][]api.Rule{{first, second}, {second, first}} {
		pol, _, err := MergeLayers([]api.Layer{{Kind: api.LayerSession, Rules: rules}})
		if err != nil || len(pol.Rules) != 2 || pol.Rules[0].ID != first.ID || pol.Rules[1].ID != first.ID {
			t.Fatal("a stable public ID silently merged distinct grant instances")
		}
		if previous != nil && !reflect.DeepEqual(previous, pol.Rules) {
			t.Fatal("equal public IDs produced nondeterministic lease ordering")
		}
		previous = pol.Rules
	}
	if same, err := EqualRuleInstances(first, second); err != nil || same {
		t.Fatal("full instance equality ignored its independent grant identity")
	}
}

func TestRuleIDSelectionClosesSessionAmbiguityWithoutChoosingARow(t *testing.T) {
	rules := fixedCollisionRules(api.LayerSession, "", "session-collision-7608.example", "session-collision-21481.example")
	if rules[0].ID != "gcqjkxl" || rules[1].ID != "gcqjkxl" {
		t.Fatal("fixed real session collision fixture changed")
	}
	before := slices.Clone(rules)
	found, err := CheckRuleIDSelection(rules, "gcqjkxl", api.LayerRef{Kind: api.LayerSession})
	requireRuleAmbiguity(t, err)
	if found || !reflect.DeepEqual(before, rules) {
		t.Fatal("ID selection chose or mutated an ambiguous row")
	}
	if found, err := CheckRuleIDSelection(rules[:1], "gcqjkxl", api.LayerRef{Kind: api.LayerSession}); err != nil || !found {
		t.Fatal("valid single-rule ID selection changed")
	}
}
