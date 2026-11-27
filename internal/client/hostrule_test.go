package client

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func hostRuleFixture() ([]Host, Tiers) {
	hosts := []Host{{Name: "box"}, {Name: "other"}, {Name: "shared"}}
	tiers := NewTiers(map[string]TierEntry{"box": {Tier: TierOwn}, "other": {Tier: TierThrowaway}, "shared": {Tier: TierShared}})
	return hosts, tiers
}

func TestHostRuleResolutionKeepsExplicitLastAndAutoAboveRules(t *testing.T) {
	hosts, tiers := hostRuleFixture()
	for _, tc := range []struct {
		argument string
		want     string
		auto     bool
	}{{"box", "box", false}, {"shared", "shared", false}, {"-", "other", false}, {"auto", "", true}} {
		in := hostRulePlanInput{argument: tc.argument, last: "other", rule: &api.HostSelectorV1{Names: []string{"not-saved"}}, ruleLayer: api.LayerRepo,
			ruleName: "forge.example/team/*", environment: "shared", saved: "not-saved"}
		got, err := resolveHostRulePlan(in, hosts, tiers)
		if err != nil || got.automatic != tc.auto || tc.want != "" && !slices.Equal(got.names, []string{tc.want}) {
			t.Fatal("a lower-priority rule or default changed an explicit choice", tc.argument, got, err)
		}
	}
}

func TestHostRuleListPreservesOrderAndCanNameSharedHosts(t *testing.T) {
	hosts, tiers := hostRuleFixture()
	rule := []string{"shared", "other", "box"}
	got, err := resolveHostRulePlan(hostRulePlanInput{rule: &api.HostSelectorV1{Names: rule}, ruleLayer: api.LayerRepo, ruleName: "forge.example/team/*", environment: "box"}, hosts, tiers)
	if err != nil || !slices.Equal(got.names, rule) || got.reason != "from the rule for forge.example/team/*" {
		t.Fatal("a rule's ordered named choices changed", got, err)
	}
	got.names[0] = "CHANGED"
	if rule[0] != "shared" {
		t.Fatal("the result borrowed its mutable rule input")
	}
	if _, err := resolveHostRulePlan(hostRulePlanInput{rule: &api.HostSelectorV1{Names: []string{"box", "missing"}}, ruleLayer: api.LayerProfile, ruleName: "review"}, hosts, tiers); err == nil {
		t.Fatal("a later unknown rule host escaped validation")
	}
}

func TestHostRuleAutoIsAWholeSelectorAndNotAnOrderedHost(t *testing.T) {
	hosts, tiers := hostRuleFixture()
	got, err := resolveHostRulePlan(hostRulePlanInput{rule: &api.HostSelectorV1{Auto: true}, ruleLayer: api.LayerProfile, ruleName: "review", saved: "box"}, hosts, tiers)
	if err != nil || !got.automatic || len(got.names) != 0 || got.reason != "from the profile review" {
		t.Fatal("a rule's auto selector became a named-host grant", got, err)
	}
	for _, selector := range []*api.HostSelectorV1{
		{Auto: true, Names: []string{"box"}},
		{Names: []string{"auto"}},
		{Names: []string{"box", "box"}},
	} {
		if _, err := resolveHostRulePlan(hostRulePlanInput{rule: selector, ruleLayer: api.LayerSession}, hosts, tiers); err == nil {
			t.Fatal("an invalid selector was accepted", selector)
		}
	}
}

func TestHostRuleDefaultsAndSoleHostAreConservative(t *testing.T) {
	hosts, tiers := hostRuleFixture()
	for _, tc := range []struct {
		in   hostRulePlanInput
		want string
		auto bool
	}{{hostRulePlanInput{environment: "other", saved: "box"}, "other", false},
		{hostRulePlanInput{saved: "box"}, "box", false},
		{hostRulePlanInput{environment: "auto", saved: "box"}, "", true},
		{hostRulePlanInput{saved: "auto"}, "", true}} {
		got, err := resolveHostRulePlan(tc.in, hosts, tiers)
		if err != nil || got.automatic != tc.auto || tc.want != "" && !slices.Equal(got.names, []string{tc.want}) {
			t.Fatal("default precedence changed", got, err)
		}
	}
	for _, in := range []hostRulePlanInput{{environment: "shared", saved: "box"}, {environment: "missing", saved: "box"}, {saved: "shared"}, {}} {
		if _, err := resolveHostRulePlan(in, hosts, tiers); err == nil {
			t.Fatal("an invalid, shared or ambiguous default silently selected another host", in)
		}
	}
	got, err := resolveHostRulePlan(hostRulePlanInput{}, []Host{hosts[0], hosts[2]}, tiers)
	if err != nil || !slices.Equal(got.names, []string{"box"}) {
		t.Fatal("the sole non-shared host was not selected", got, err)
	}
}

func TestHostRuleResolutionNeverReadsLabelsOrChangesGrantPolicy(t *testing.T) {
	hosts, tiers := hostRuleFixture()
	in := hostRulePlanInput{rule: &api.HostSelectorV1{Names: []string{"shared", "box"}}, ruleLayer: api.LayerSession}
	before, err := resolveHostRulePlan(in, hosts, tiers)
	if err != nil {
		t.Fatal(err)
	}
	for i := range hosts {
		hosts[i].Labels = map[string]string{"tier": "own", "pool": "true", "location": "elsewhere", "preferred": "yes"}
	}
	after, err := resolveHostRulePlan(in, hosts, tiers)
	if err != nil || !reflect.DeepEqual(before, after) || tiers.Of("shared") != TierShared {
		t.Fatal("labels changed selection or the stored tier", before, after, err)
	}
	if _, err := resolveHostRulePlan(hostRulePlanInput{saved: "shared"}, hosts, tiers); err == nil {
		t.Fatal("a label made a shared host eligible as a default")
	}
}

func TestHostRuleErrorsCarryCodesHintsAndSafeReasons(t *testing.T) {
	hosts, tiers := hostRuleFixture()
	for _, in := range []hostRulePlanInput{
		{argument: "-"},
		{rule: &api.HostSelectorV1{}, ruleLayer: api.LayerSession},
		{rule: &api.HostSelectorV1{Names: []string{"box"}}, ruleLayer: api.LayerAll},
		{rule: &api.HostSelectorV1{Names: []string{"auto", "box"}}, ruleLayer: api.LayerSession},
	} {
		_, err := resolveHostRulePlan(in, hosts, tiers)
		var user *UserError
		if !errors.As(err, &user) || user.Code == "" || user.Hint == "" {
			t.Fatal("a host selection failure has no actionable typed error", in, err)
		}
	}
	got, err := resolveHostRulePlan(hostRulePlanInput{rule: &api.HostSelectorV1{Names: []string{"box"}}, ruleLayer: api.LayerRepo, ruleName: "https://hidden:credential@forge.example/team/*"}, hosts, tiers)
	if err != nil || strings.Contains(got.reason, "credential") || got.reason != "from the rule for forge.example/team/*" {
		t.Fatal("a reason retained repository credentials", got, err)
	}
	_, err = resolveHostRulePlan(hostRulePlanInput{}, hosts, tiers)
	if err == nil || !strings.Contains(err.Error(), "box, other, shared") {
		t.Fatal("the missing-host error did not list the saved names", err)
	}
}

func TestHostRuleDefaultMarkerIsOnlyPresentationForActualDefaults(t *testing.T) {
	hosts, tiers := hostRuleFixture()
	for _, test := range []struct {
		in   hostRulePlanInput
		want bool
	}{
		{hostRulePlanInput{argument: "box", saved: "box"}, false},
		{hostRulePlanInput{argument: "auto"}, false},
		{hostRulePlanInput{rule: &api.HostSelectorV1{Names: []string{"box"}}, ruleLayer: api.LayerSession}, false},
		{hostRulePlanInput{rule: &api.HostSelectorV1{Auto: true}, ruleLayer: api.LayerSession}, false},
		{hostRulePlanInput{saved: "box"}, true},
		{hostRulePlanInput{environment: "other", saved: "box"}, true},
		{hostRulePlanInput{saved: "auto"}, true},
	} {
		plan, err := resolveHostRulePlan(test.in, hosts, tiers)
		if err != nil || plan.defaulted != test.want {
			t.Fatal("the UI default marker changed the selection's origin", test.in, plan, err)
		}
	}
	plan, err := resolveHostRulePlan(hostRulePlanInput{}, []Host{hosts[0], hosts[2]}, tiers)
	if err != nil || plan.defaulted {
		t.Fatal("the sole eligible host pretended to be a saved default", plan, err)
	}
}
