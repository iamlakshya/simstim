package egress

import (
	"slices"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
)

func canonicalAgentKinds(kinds []string) ([]string, error) {
	known := make(map[string]struct{}, len(kinds))
	for _, kind := range kinds {
		if _, ok := agents.Find(kind); !ok {
			return nil, memberError("bad_request", "unknown agent kind in trusted membership", "Use only canonical agent-table kinds in the trusted saved set.")
		}
		known[kind] = struct{}{}
	}
	result := make([]string, 0, len(known))
	for kind := range known {
		result = append(result, kind)
	}
	slices.Sort(result)
	return result, nil
}

// NewEngineForAgents extends only the builtin agent egress set. Locks, user
// rules, ordered layers, methods and once accounting use the same constructor
// and evaluator as NewEngine. The kind set is finite canonical agent data.
func NewEngineForAgents(kinds []string, policy api.Policy) (*Engine, error) {
	kinds, err := canonicalAgentKinds(kinds)
	if err != nil {
		return nil, err
	}
	var rules []compiledRule
	for _, kind := range kinds {
		entries, err := compileAgentEgress(kind)
		if err != nil {
			return nil, err
		}
		rules = append(rules, entries...)
	}
	legacyKind := ""
	if len(kinds) == 1 {
		legacyKind = kinds[0]
	}
	return newEngineWithAgentRules(legacyKind, rules, policy)
}
