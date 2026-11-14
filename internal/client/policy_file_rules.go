package client

import (
	"bytes"
	"reflect"

	"github.com/BurntSushi/toml"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

func editStructuredFileRules(network map[string]any, rule api.Rule, remove string) (changed bool, removed int, exists bool, err error) {
	key := schemaKey(network, reflect.TypeFor[api.NetworkValues](), "rules")
	var entries []any
	switch raw := network[key].(type) {
	case []map[string]any:
		for _, entry := range raw {
			entries = append(entries, entry)
		}
	case []any:
		entries = raw
	}
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(map[string]any{"network": map[string]any{"rules": entries}}); err != nil {
		return false, 0, false, err
	}
	var values api.LayerValues
	if _, err := decodeConfig(encoded.String(), &values); err != nil {
		return false, 0, false, err
	}
	var kept []any
	for index, raw := range values.Network.Rules {
		canonical, err := egress.CanonicalNetworkRule(raw)
		if err != nil {
			return false, 0, false, policyError(err)
		}
		candidate := api.Rule{Layer: rule.Layer, Name: rule.Name, Door: api.DoorNet, Allow: canonical.Allow, Target: canonical.Target, Methods: canonical.Methods}
		candidate.ID = api.RuleID(candidate)
		if remove != "" && (remove == candidate.ID || remove == candidate.Target) {
			changed = true
			removed++
			continue
		}
		if !canonical.Remove && candidate.ID == rule.ID {
			exists = true
		}
		kept = append(kept, entries[index])
	}
	if remove == "" && rule.Methods != nil && !exists {
		kept = append(kept, map[string]any{"target": rule.Target, "allow": rule.Allow, "methods": *rule.Methods})
		changed = true
		exists = true
	}
	if len(entries) > 0 || len(kept) > 0 {
		network[key] = kept
	}
	return changed, removed, exists, nil
}
