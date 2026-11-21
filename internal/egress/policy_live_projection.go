package egress

import (
	"reflect"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

// PrepareLivePolicyV1 validates one protected source packet for the existing
// policy publisher. It creates no Engine, ledger, native Need or secret grant.
// Only the original supervisor can preserve and submit its non-laptop layers.
func PrepareLivePolicyV1(applied api.Policy, sources []api.Layer) (api.Policy, string, []string, error) {
	hash, err := api.HashPolicyLaptopLayersV1(sources)
	if err != nil {
		return api.Policy{}, "", nil, err
	}
	desired, clean, err := MergeLayers(api.CloneLayers(sources))
	if err != nil {
		return api.Policy{}, "", nil, err
	}
	canonical, err := api.HashPolicyLaptopLayersV1(clean)
	if err != nil || canonical != hash {
		return api.Policy{}, "", nil, memberError("policy_sources", "live policy sources are not canonical", "Refresh the selected laptop layers before applying them.")
	}
	if err := validateLivePushCarryV1(applied, desired); err != nil {
		return api.Policy{}, "", nil, err
	}
	live, pending := projectLivePolicyV1(applied, desired)
	return live, hash, pending, nil
}

func projectLivePolicyV1(applied, desired api.Policy) (api.Policy, []string) {
	live := api.ClonePolicy(desired)
	previous := api.ClonePolicy(applied)
	live.Sources = nil
	live.Sandbox = previous.Sandbox
	live.Sandbox.Preset, live.Sandbox.Push = desired.Sandbox.Preset, desired.Sandbox.Push
	live.Rules = append(nonliveRulesV1(previous.Rules), liveRulesV1(live.Rules)...)
	live.Locks = projectLiveLocksV1(previous.Locks, live.Locks)
	live.Origins = nil
	for _, origin := range previous.Origins {
		if !livePolicyKeyV1(origin.Key) {
			live.Origins = append(live.Origins, origin)
		}
	}
	for _, origin := range desired.Origins {
		if livePolicyKeyV1(origin.Key) {
			live.Origins = append(live.Origins, origin)
		}
	}
	pending := nonliveSandboxChangesV1(applied.Sandbox, desired.Sandbox)
	if !reflect.DeepEqual(nonliveRulesV1(applied.Rules), nonliveRulesV1(desired.Rules)) {
		pending = append(pending, "rules")
	}
	if !reflect.DeepEqual(live.Locks, desired.Locks) {
		pending = append(pending, "locks")
	}
	slices.Sort(pending)
	return api.ClonePolicy(live), pending
}

func livePolicyKeyV1(key string) bool {
	return strings.HasPrefix(key, "network.") || strings.HasPrefix(key, "classifier.") || key == "sandbox.preset" || key == "sandbox.push"
}

func liveRuleV1(rule api.Rule) bool {
	return rule.Door == "" || rule.Door == api.DoorNet || rule.Door == api.DoorPush
}

func liveRulesV1(rules []api.Rule) []api.Rule {
	var selected []api.Rule
	for _, rule := range rules {
		if liveRuleV1(rule) {
			selected = append(selected, rule)
		}
	}
	return selected
}

func nonliveRulesV1(rules []api.Rule) []api.Rule {
	var selected []api.Rule
	for _, rule := range rules {
		if !liveRuleV1(rule) {
			selected = append(selected, rule)
		}
	}
	return selected
}

func nonliveSandboxChangesV1(applied, desired api.SandboxValues) []string {
	before, after := reflect.ValueOf(applied), reflect.ValueOf(desired)
	var keys []string
	for index := range before.NumField() {
		field := before.Type().Field(index)
		key, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if key != "" && key != "-" && key != "preset" && key != "push" && !reflect.DeepEqual(before.Field(index).Interface(), after.Field(index).Interface()) {
			keys = append(keys, "sandbox."+key)
		}
	}
	return keys
}

// A generic source replacement cannot create the original forge visibility
// review or public acknowledgement that a new positive push grant requires.
func validateLivePushCarryV1(applied, desired api.Policy) error {
	refuse := func() error {
		return memberError("push_approval", "live source replacement cannot add push authority", "Use the original forge-reviewed push approval before applying this policy.")
	}
	if desired.Sandbox.Push != nil && *desired.Sandbox.Push && (applied.Sandbox.Push == nil || !*applied.Sandbox.Push) {
		return refuse()
	}
	for _, candidate := range desired.Rules {
		if candidate.Door != api.DoorPush || !candidate.Allow {
			continue
		}
		found := false
		for _, current := range applied.Rules {
			if current.Door != api.DoorPush || !current.Allow {
				continue
			}
			equal, err := EqualRuleInstances(current, candidate)
			if err != nil {
				return err
			}
			if equal {
				found = true
				break
			}
		}
		if !found {
			return refuse()
		}
	}
	return nil
}
