package egress

import (
	"net/netip"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

// This is a transaction-local selection over the existing finite capability
// set and canonical eligible rules, never a matcher, cache or second ledger.
// Permanent/owned coverage is free. Among available once instances it selects
// the fewest additional slots; equal plans retain canonical existing order.
// No deadline ordering or new grant state is introduced.
func (e *Engine) modelCapabilityPlanLocked(request Request, features []string, permit *Permit, mode evalMode) ([]*compiledRule, string) {
	features, err := api.CanonicalModelTools(features)
	if err != nil {
		return nil, "model policy"
	}
	for _, feature := range features {
		if !e.modelToolAllowedLocked(request, feature, permit, mode) {
			return nil, feature
		}
	}
	if len(features) == 0 {
		return nil, ""
	}
	type candidate struct {
		rule *compiledRule
		mask int
	}
	var candidates []candidate
	free := 0
	var freeRules []*compiledRule
	for _, layer := range e.layers {
		for i := range layer.rules {
			rule := &layer.rules[i]
			if !rule.allow || len(rule.tools) == 0 || !matchesRuleMethod(*rule, request.Method) || !e.matchesLocked(*rule, request.Host, netip.Addr{}, false, request.Port, request.Path, request.Now) {
				continue
			}
			rank, available := e.oncePriority(*rule, permit, mode)
			if !available {
				continue
			}
			mask := 0
			for bit, feature := range features {
				if slices.Contains(rule.tools, feature) {
					mask |= 1 << bit
				}
			}
			if rank < 2 {
				free |= mask
				if mask != 0 && rule.publicOnly {
					freeRules = append(freeRules, rule)
				}
			} else if mask != 0 {
				candidates = append(candidates, candidate{rule, mask})
			}
		}
	}
	type selection struct {
		rules []*compiledRule
		found bool
	}
	// CanonicalModelTools bounds this table to 64 states (six actual names).
	states := 1 << len(features)
	plans := make([]selection, states)
	// Free coverage carries its rule restrictions even though it claims no
	// new once slot. All DP states start with this same free plan, preserving
	// the existing minimum-additional-slots comparison.
	plans[free] = selection{rules: freeRules, found: true}
	for _, candidate := range candidates {
		for mask := 0; mask < states; mask++ {
			old := plans[mask]
			if !old.found {
				continue
			}
			next := mask | candidate.mask
			if next == mask || plans[next].found && len(plans[next].rules) <= len(old.rules)+1 {
				continue
			}
			rules := append(slices.Clone(old.rules), candidate.rule)
			plans[next] = selection{rules: rules, found: true}
		}
	}
	selected := plans[states-1]
	if !selected.found {
		return nil, "model policy"
	}
	return selected.rules, ""
}
