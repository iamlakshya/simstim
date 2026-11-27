package egress

import (
	"net/url"
	"sort"

	"github.com/iamlakshya/simstim/internal/api"
)

func registryRedaction(state *memberState) (*auditSecrets, error) {
	type namedValue struct {
		name  string
		value api.Secret
	}
	var values []namedValue
	for name, secret := range state.secrets {
		if secret.present {
			values = append(values, namedValue{name, secret.value})
		}
		for _, value := range secret.history {
			values = append(values, namedValue{name, value})
		}
		if secret.bound != nil {
			values = append(values, namedValue{name, secret.bound.sentinel})
		}
		for _, mint := range secret.minted {
			values = append(values, namedValue{name, mint.value}, namedValue{name, mint.sentinel})
		}
	}
	for name, entries := range state.retired {
		for _, entry := range entries {
			values = append(values, namedValue{name, entry.sentinel})
		}
	}
	for _, tripwire := range state.decoys {
		for _, value := range tripwire.Values {
			for _, form := range decoyValueForms(value) {
				values = append(values, namedValue{"decoy " + tripwire.Name, form})
			}
		}
	}
	sort.SliceStable(values, func(i, j int) bool {
		if len(values[i].value) != len(values[j].value) {
			return len(values[i].value) > len(values[j].value)
		}
		if values[i].name != values[j].name {
			return values[i].name < values[j].name
		}
		return values[i].value < values[j].value
	})
	seen := make(map[string]bool)
	var pairs []secretPair
	for _, value := range values {
		if value.value == "" {
			continue
		}
		text := string(value.value)
		for _, needle := range []string{text, url.PathEscape(text), url.QueryEscape(text)} {
			if !seen[needle] {
				seen[needle] = true
				pairs = append(pairs, secretPair{needle: needle, tag: "[" + value.name + "]", name: value.name})
			}
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool { return len(pairs[i].needle) > len(pairs[j].needle) })
	return newAuditSecrets(pairs), nil
}
