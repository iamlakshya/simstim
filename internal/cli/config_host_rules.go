package cli

import (
	"maps"
	"reflect"
	"slices"
	"strconv"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

type checkedHostRuleReferenceV1 struct {
	path   keyPath
	choice api.HostSelectorV1
}

// This semantic pass checks every profile and repository rule, including
// unused ones. It reads the saved host set once. The ordinary schema/type pass
// still owns malformed and forbidden keys and the actual selector grammar.
func (c *checker) hostRuleReferencesV1(tree map[string]any, typ reflect.Type) {
	if typ != reflect.TypeFor[client.Settings]() {
		return
	}
	var references []checkedHostRuleReferenceV1
	for _, root := range client.InspectConfigTable(tree, typ).Keys {
		if root.Type == nil || root.Conflict != "" || root.Name != "profiles" && root.Name != "repos" {
			continue
		}
		named, ok := tree[root.Spelling].(map[string]any)
		if !ok {
			continue
		}
		for _, name := range slices.Sorted(maps.Keys(named)) {
			layer, ok := named[name].(map[string]any)
			if !ok {
				continue
			}
			path := keyPath(nil).field(root.Spelling, root.Name).add(name)
			references = append(references, hostRuleReferencesInLayerV1(layer, path)...)
		}
	}
	if len(references) == 0 {
		return
	}
	hosts, err := client.LoadHosts()
	if err != nil {
		for _, rule := range references {
			c.add(rule.path, finding{Code: "host_rule_hosts", Message: "the saved hosts could not be read to check this host rule. Fix hosts.toml, then check again"})
		}
		return
	}
	saved := make(map[string]int, len(hosts))
	for _, host := range hosts {
		saved[host.Name]++
	}
	for _, rule := range references {
		for _, name := range rule.choice.Names {
			switch saved[name] {
			case 0:
				c.add(rule.path, finding{Code: "host_not_found", Message: "host " + strconv.Quote(name) + " in this rule is not saved. Add it or choose a saved host"})
			case 1:
			default:
				c.add(rule.path, finding{Code: "config_unusable", Message: "host " + strconv.Quote(name) + " in this rule has more than one saved entry. Remove the duplicate"})
			}
		}
	}
}

func hostRuleReferencesInLayerV1(layer map[string]any, path keyPath) []checkedHostRuleReferenceV1 {
	var result []checkedHostRuleReferenceV1
	for _, sandbox := range client.InspectConfigTable(layer, reflect.TypeFor[api.LayerValues]()).Keys {
		if sandbox.Type == nil || sandbox.Conflict != "" || sandbox.Name != "sandbox" {
			continue
		}
		table, ok := layer[sandbox.Spelling].(map[string]any)
		if !ok {
			continue
		}
		for _, host := range client.InspectConfigTable(table, reflect.TypeFor[api.SandboxValues]()).Keys {
			if host.Type == nil || host.Conflict != "" || host.Name != "host" {
				continue
			}
			choice, err := api.DecodeHostSelectorV1(table[host.Spelling])
			if err != nil || choice.Validate() != nil || choice.Auto {
				continue
			}
			result = append(result, checkedHostRuleReferenceV1{path: path.field(sandbox.Spelling, sandbox.Name).field(host.Spelling, host.Name), choice: choice})
		}
	}
	return result
}
