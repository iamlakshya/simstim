package egress

import (
	"fmt"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/nettarget"
	"github.com/iamlakshya/simstim/internal/proto"
)

// NormaliseHost validates and canonicalises a request hostname or IP literal.
func NormaliseHost(raw string) (string, error) {
	host, err := nettarget.NormaliseHost(raw)
	if err != nil {
		return "", fmt.Errorf("normalise network host: %w", err)
	}
	return host, nil
}

// ValidateRuleTarget checks and canonicalises a net rule target: a hostname,
// "*.domain", an IP literal or CIDR (short forms such as "10/8" included),
// with an optional ":port" or "/path".
func ValidateRuleTarget(raw string) (string, error) {
	cr, err := compileTarget(raw, true)
	if err != nil {
		return "", &proto.Error{
			Code:    "bad_target",
			Message: fmt.Sprintf("invalid network target %q: %v", raw, err),
			Hint:    "Pass a host such as api.example.com, a wildcard such as *.example.com, or an address or CIDR.",
		}
	}
	return formatCompiledTarget(cr), nil
}

func formatCompiledTarget(cr compiledRule) string {
	return (nettarget.Target{Host: cr.host, Port: cr.port, Path: cr.path, Wildcard: cr.wildcard, Prefix: cr.prefix}).String()
}

func compileRule(r api.Rule) (compiledRule, error) {
	methods, err := canonicalRuleMethods(r)
	if err != nil {
		return compiledRule{}, err
	}
	tools, err := canonicalRuleTools(r)
	if err != nil {
		return compiledRule{}, err
	}
	r.Tools = tools
	cr, err := compileTarget(r.Target, r.Allow)
	if err != nil {
		return compiledRule{}, &proto.Error{
			Code:    "bad_target",
			Message: fmt.Sprintf("invalid network target %q: %v", r.Target, err),
			Hint:    "Pass a host such as api.example.com, a wildcard such as *.example.com, or an address or CIDR.",
		}
	}
	id := r.ID
	if methods != nil || len(tools) != 0 {
		r.Methods, r.Target = methods, formatCompiledTarget(cr)
		canonicalID := api.RuleID(r)
		if id != "" && id != canonicalID {
			return compiledRule{}, &proto.Error{Code: "bad_rule_id", Message: "method-qualified rule has a noncanonical ID", Hint: "Remove the supplied rule ID and submit the rule again."}
		}
		id = canonicalID
	}
	if id == "" {
		rCopy := r
		rCopy.Target = formatCompiledTarget(cr)
		id = api.RuleID(rCopy)
	}
	cr.id = id
	cr.methods = methods
	cr.tools = tools
	cr.ref = api.LayerRef{Kind: r.Layer, Name: r.Name, Source: r.Source}
	cr.allow = r.Allow
	cr.once = r.Once
	cr.until = canonicalInstant(r.Until)
	cr.grantID = r.GrantID
	return cr, nil
}

func compileTarget(raw string, allow bool) (compiledRule, error) {
	target, err := nettarget.Parse(raw)
	if err != nil {
		return compiledRule{}, fmt.Errorf("parse network target: %w", err)
	}
	return compiledRule{allow: allow, host: target.Host, port: target.Port, path: target.Path,
		pathPrefix: strings.HasSuffix(target.Path, "/"), wildcard: target.Wildcard,
		isNumeric: target.Prefix.IsValid(), prefix: target.Prefix}, nil
}

func splitTargetPath(raw string) (string, string, error) {
	hostPort, path, err := nettarget.SplitPath(raw)
	if err != nil {
		return "", "", fmt.Errorf("split rule path: %w", err)
	}
	return hostPort, path, nil
}

func splitTargetPort(raw string) (string, int, error) {
	base, port, err := nettarget.SplitPort(raw)
	if err != nil {
		return "", 0, fmt.Errorf("split rule port: %w", err)
	}
	return base, port, nil
}

type lockedRule struct {
	layer  api.LayerKind
	target string
	rule   compiledRule
}

// MergeLayers validates and merges policy layers from broadest to narrowest,
// enforcing locks and canonicalising rules (spec 8.2).
func MergeLayers(layers []api.Layer) (api.Policy, []api.Layer, error) {
	sorted := slices.Clone(layers)
	seen := map[api.LayerRef]bool{}
	kinds := map[api.LayerKind]bool{}
	for i := range sorted {
		l := &sorted[i]
		ref := api.LayerRef{Kind: l.Kind, Name: l.Name, Source: l.Source}
		if !api.ValidLayerRef(ref) || seen[ref] || kinds[l.Kind] && l.Kind != api.LayerRepo {
			return api.Policy{}, nil, &proto.Error{Code: "bad_request", Message: "policy layers must have known kinds and unique names within each kind", Hint: "Send each policy layer once."}
		}
		seen[ref], kinds[l.Kind] = true, true
		if err := canonicalLayerValues(l); err != nil {
			return api.Policy{}, nil, err
		}
	}
	slices.SortStableFunc(sorted, func(a, b api.Layer) int { return api.LayerRank(a.Kind) - api.LayerRank(b.Kind) })
	values, origins, locks, err := resolveLayerValues(sorted)
	if err != nil {
		return api.Policy{}, nil, err
	}
	pol := api.Policy{LayerValues: values, Origins: origins, Locks: locks, Preset: api.PresetBalanced, Unknown: api.UnknownAsk}
	if values.Sandbox.Preset != nil {
		pol.Preset = api.Preset(*values.Sandbox.Preset)
	}
	if values.Network.Unknown != nil {
		pol.Unknown = api.Unknown(*values.Network.Unknown)
	}
	var denies []lockedRule
	for _, lk := range locks {
		for _, target := range lk.Deny {
			cr, _ := compileTarget(target, false)
			denies = append(denies, lockedRule{layer: lk.Layer, target: target, rule: cr})
		}
		for _, raw := range lk.Network.Rules {
			cr, err := compileRule(api.Rule{Layer: lk.Layer, Name: lk.Name, Door: api.DoorNet, Target: raw.Target, Methods: raw.Methods})
			if err != nil {
				return api.Policy{}, nil, err
			}
			denies = append(denies, lockedRule{layer: lk.Layer, target: raw.Target, rule: cr})
		}
	}
	for i, l := range sorted {
		pol.Rules, err = applyNetworkRemovals(pol.Rules, l, locks)
		if err != nil {
			return api.Policy{}, nil, err
		}
		applicable := slices.DeleteFunc(slices.Clone(denies), func(d lockedRule) bool { return api.LayerRank(d.layer) > api.LayerRank(l.Kind) })
		rules, err := cleanLayerRules(l, applicable)
		if err != nil {
			return api.Policy{}, nil, err
		}
		sorted[i].Rules = rules
		sorted[i].Network.Allow = nil
		sorted[i].Network.Deny = nil
		pol.Rules = append(pol.Rules, rules...)
		pol.Order = append(pol.Order, api.LayerRef{Kind: l.Kind, Name: l.Name, Source: l.Source})
	}
	if err := ValidateRuleIdentities(pol.Rules); err != nil {
		return api.Policy{}, nil, err
	}
	return pol, sorted, nil
}

func cleanLayerRules(l api.Layer, denies []lockedRule) ([]api.Rule, error) {
	dedup := map[ruleInstanceIdentity]api.Rule{}
	for _, r := range l.Rules {
		r.Layer, r.Name, r.Source = l.Kind, l.Name, l.Source
		var err error
		r, err = canonicalIdentityRule(r)
		if err != nil {
			return nil, err
		}
		if r.Door == api.DoorNet {
			if r.Allow {
				cr, _ := compileTarget(r.Target, true)
				cr.methods = r.Methods
				if by, target, ok := coveredByLockedDeny(cr, denies); ok {
					return nil, &proto.Error{Code: "policy_locked",
						Message: fmt.Sprintf("a lock in %s denies %s, so no layer can allow %s", by, target, r.Target),
						Hint:    "Remove " + target + " from [lock] network.deny."}
				}
			}
		}
		canonicalID := api.RuleID(r)
		if (r.Methods != nil || len(r.Tools) != 0) && r.ID != "" && r.ID != canonicalID {
			return nil, &proto.Error{Code: "bad_rule_id", Message: "method-qualified rule has a noncanonical ID", Hint: "Remove the supplied rule ID and submit the rule again."}
		}
		r.ID = canonicalID
		dedup[ruleInstanceKey(r)] = r
	}
	if len(dedup) == 0 {
		return nil, nil
	}
	out := make([]api.Rule, 0, len(dedup))
	for _, r := range dedup {
		out = append(out, r)
	}
	slices.SortFunc(out, compareRuleInstances)
	return out, nil
}

func coveredByLockedDeny(allow compiledRule, denies []lockedRule) (api.LayerKind, string, bool) {
	allowPort := allow.port
	if allowPort == 0 {
		allowPort = 443
	}
	for _, d := range denies {
		if !api.HTTPMethodSetsIntersect(allow.methods, d.rule.methods) {
			continue
		}
		if d.rule.port != 0 && d.rule.port != allowPort {
			continue
		}
		if !coversRulePath(d.rule, allow) {
			continue
		}
		switch {
		case d.rule.isNumeric && allow.isNumeric && d.rule.prefix.Overlaps(allow.prefix) && d.rule.prefix.Bits() <= allow.prefix.Bits():
			return d.layer, d.target, true
		case !d.rule.isNumeric && !allow.isNumeric && d.rule.wildcard && strings.HasSuffix(allow.host, "."+d.rule.host):
			return d.layer, d.target, true
		case !d.rule.isNumeric && !allow.isNumeric && !d.rule.wildcard && !allow.wildcard && d.rule.host == allow.host:
			return d.layer, d.target, true
		case !d.rule.isNumeric && !allow.isNumeric && d.rule.wildcard && allow.wildcard && (d.rule.host == allow.host || strings.HasSuffix(allow.host, "."+d.rule.host)):
			return d.layer, d.target, true
		}
	}
	return "", "", false
}
