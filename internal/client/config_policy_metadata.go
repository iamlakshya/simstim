package client

import (
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
)

// PolicyCompletionMetadata is owned, local-only configuration metadata.
// It selects no repository, resolves no folder and grants no policy action.
type PolicyCompletionMetadata struct {
	Profiles []string
	Rules    []api.Rule
}

// DecodePolicyCompletionMetadata consumes data from the caller's existing
// bounded local completion reader. It uses the ONE canonical decoder and
// actual Settings/hostsFile types; it reads/writes no file or process.
// The CLI retains its existing four-MiB read limit and quiet-error behavior.
func DecodePolicyCompletionMetadata(settingsData, hostsData string) (PolicyCompletionMetadata, error) {
	var settings Settings
	if _, err := decodeConfig(settingsData, &settings); err != nil {
		return PolicyCompletionMetadata{}, err
	}
	var hosts hostsFile
	if _, err := decodeConfig(hostsData, &hosts); err != nil {
		return PolicyCompletionMetadata{}, err
	}
	return CollectPolicyCompletionMetadata(settings, hosts.Hosts)
}

// CollectPolicyCompletionMetadata visits ALL configured file layers once,
// including unselected repositories/profiles and all saved hosts. Rules use
// the SAME merger/P22 normaliser and api.RuleID, never a completion parser.
// This pure adapter performs no git/provider/network/lock/environment lookup.
func CollectPolicyCompletionMetadata(settings Settings, hosts []Host) (PolicyCompletionMetadata, error) {
	var out PolicyCompletionMetadata
	if err := ValidateSettings(settings); err != nil {
		return out, err
	}
	profiles := slices.Sorted(maps.Keys(settings.Profiles))
	unique := map[policyCompletionRuleKey]api.Rule{}
	add := func(layer api.Layer) error {
		// Validate the actual complete layer once. Effective Rules collapse
		// short-ID collisions, so enumerate physical file rows separately.
		if err := egress.ValidateLayerValues(layer.LayerValues); err != nil {
			return policyError(err)
		}
		// Display metadata is not policy-wide short-ID admission. Validate
		// scalar/lock shape once, then each physical tuple below. Root's
		// authority merger correctly rejects collisions; listing must keep
		// every colliding description so the user can repair the source.
		values := layer
		values.Network.Allow, values.Network.Deny, values.Network.Rules, values.Rules = nil, nil, nil, nil
		if _, _, err := egress.MergeLayers(api.CloneLayers([]api.Layer{values})); err != nil {
			return policyError(err)
		}
		row := func(raw api.NetworkRule) error {
			value, err := egress.CanonicalNetworkRule(raw)
			if err != nil {
				return policyError(err)
			}
			rule := api.Rule{Layer: layer.Kind, Name: layer.Name, Source: layer.Source, Door: api.DoorNet, Allow: value.Allow, Target: value.Target, Methods: value.Methods}
			rule.ID = api.RuleID(rule)
			unique[completionRuleKey(rule)] = rule
			return nil
		}
		for _, target := range layer.Network.Allow {
			if err := row(api.NetworkRule{Target: target, Allow: true}); err != nil {
				return err
			}
		}
		for _, target := range layer.Network.Deny {
			if err := row(api.NetworkRule{Target: target}); err != nil {
				return err
			}
		}
		for _, value := range layer.Network.Rules {
			if err := row(value); err != nil {
				return err
			}
		}
		return nil
	}
	if err := add(api.Layer{Kind: api.LayerAll, LayerValues: settings.LayerValues, Lock: policyLock(settings.Lock)}); err != nil {
		return out, err
	}
	for _, host := range hosts {
		if err := add(api.Layer{Kind: api.LayerHost, Name: host.Name, LayerValues: host.LayerValues, Lock: policyLock(host.Lock)}); err != nil {
			return out, err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(settings.Repos)) {
		if err := add(api.Layer{Kind: api.LayerRepo, Name: name, LayerValues: repoLayerValues(settings.Repos[name])}); err != nil {
			return out, err
		}
	}
	for _, name := range profiles {
		values := settings.Profiles[name]
		// This is the actual resolver's file-profile restriction, not a
		// new name/shape parser or selected-profile inference.
		if values.Sandbox.Profile != nil {
			return out, &UserError{Msg: "a profile may not select another profile", Hint: brand.Name + " config check", Code: "layer_key", Kind: KindUsage}
		}
		if err := add(api.Layer{Kind: api.LayerProfile, Name: name, LayerValues: values}); err != nil {
			return out, err
		}
	}
	out.Profiles = profiles
	for _, rule := range unique {
		out.Rules = append(out.Rules, rule)
	}
	slices.SortFunc(out.Rules, func(a, b api.Rule) int {
		if order := strings.Compare(a.ID, b.ID); order != 0 {
			return order
		}
		if order := strings.Compare(a.Target, b.Target); order != 0 {
			return order
		}
		if order := strings.Compare(string(a.Layer), string(b.Layer)); order != 0 {
			return order
		}
		if order := strings.Compare(a.Name, b.Name); order != 0 {
			return order
		}
		if a.Allow != b.Allow {
			if a.Allow {
				return 1
			}
			return -1
		}
		ak, bk := completionRuleKey(a), completionRuleKey(b)
		if ak.methodsPresent != bk.methodsPresent {
			if ak.methodsPresent {
				return 1
			}
			return -1
		}
		if order := strings.Compare(ak.methods, bk.methods); order != 0 {
			return order
		}
		return strings.Compare(ak.tools, bk.tools)
	})
	return out, nil
}

// The short displayed RuleID is not equality. Keep explicit comparable
// canonical fields so new mutable Rule.Tools never becomes a map key by
// pointer or makes the entire struct uncomparable. Tools is a canonical set;
// empty/absent retains legacy identity, unlike constrained Methods presence.
type policyCompletionRuleKey struct {
	source         api.LayerSource
	grantID        string
	layer          api.LayerKind
	name           string
	allow          bool
	door           api.Door
	target, mode   string
	once           bool
	until          time.Time
	methodsPresent bool
	methods, tools string
}

func completionRuleKey(rule api.Rule) policyCompletionRuleKey {
	key := policyCompletionRuleKey{source: rule.Source, grantID: rule.GrantID, layer: rule.Layer, name: rule.Name, allow: rule.Allow, door: rule.Door, target: rule.Target, mode: rule.Mode, once: rule.Once, until: rule.Until, methodsPresent: rule.Methods != nil, tools: strings.Join(rule.Tools, "\x00")}
	if rule.Methods != nil {
		key.methods = strings.Join(*rule.Methods, "\x00")
	}
	return key
}
