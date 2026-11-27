package agents

import (
	"errors"
	"slices"
	"unicode"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/secretbind"
)

// SecretDefault is unanimous local metadata, never a fetch name or grant.
// Store is deliberately absent. Only the selected member's row supplies it.
type SecretDefault struct {
	Bind   api.SecretBind
	Prefix string
}

type secretRowKey struct{ kind, env string }
type secretDefaultRestriction struct{ binding, prefix bool }

// Explicit first-class data remains available through SecretBindings. These
// unverified fields cannot become a global fallback for another agent. The
// selected first-class path and its manual acceptance gates remain distinct.
var secretDefaultRestrictions = map[secretRowKey]secretDefaultRestriction{
	{"claude", "ANTHROPIC_API_KEY"}:       {prefix: true},
	{"claude", "CLAUDE_CODE_OAUTH_TOKEN"}: {binding: true, prefix: true},
}

// GlobalSecretDefaults derives an owned immutable view from local data only.
// Ambiguous or unverified bindings are absent and need explicit config.
func GlobalSecretDefaults() (map[string]SecretDefault, error) {
	return globalSecretDefaults(Table)
}

func globalSecretDefaults(table []Agent) (map[string]SecretDefault, error) {
	type candidate struct {
		value           SecretDefault
		rawPrefix       string
		bindingEligible bool
		prefixEligible  bool
	}
	seen := make(map[string]candidate)
	for _, agent := range table {
		for _, row := range agent.Secrets {
			if !validSecretDefaultName(row.Env) || !utf8.ValidString(row.Prefix) {
				return nil, errors.New("agent secret metadata has an invalid name or prefix")
			}
			for _, r := range row.Prefix {
				if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
					return nil, errors.New("agent secret metadata has an invalid prefix")
				}
			}
			restriction := secretDefaultRestrictions[secretRowKey{agent.Kind, row.Env}]
			complete := len(row.Hosts) > 0 && row.Header != "" && row.Format != "" && row.Source != ""
			var binding api.SecretBind
			if len(row.Hosts) > 0 && row.Header != "" && row.Format != "" {
				var err error
				binding, err = secretbind.Canonical(api.SecretBind{Hosts: row.Hosts, Header: row.Header, Format: row.Format})
				if err != nil {
					return nil, errors.New("agent secret metadata has an invalid HTTPS binding")
				}
			}
			current := candidate{value: SecretDefault{Bind: binding, Prefix: row.Prefix}, rawPrefix: row.Prefix,
				bindingEligible: complete && !restriction.binding, prefixEligible: complete && !restriction.prefix}
			if previous, exists := seen[row.Env]; exists {
				identical := equalSecretDefault(previous.value.Bind, binding) && previous.rawPrefix == row.Prefix
				current.bindingEligible = current.bindingEligible && previous.bindingEligible && identical
				current.prefixEligible = current.prefixEligible && previous.prefixEligible && identical
			}
			seen[row.Env] = current
		}
	}
	output := make(map[string]SecretDefault)
	for name, current := range seen {
		if !current.bindingEligible {
			continue
		}
		value := current.value
		value.Bind.Hosts = slices.Clone(value.Bind.Hosts)
		if !current.prefixEligible {
			value.Prefix = ""
		}
		output[name] = value
	}
	return output, nil
}

func equalSecretDefault(a, b api.SecretBind) bool {
	return a.Header == b.Header && a.Format == b.Format && slices.Equal(a.Hosts, b.Hosts)
}

func validSecretDefaultName(name string) bool {
	if name == "" {
		return false
	}
	for index, c := range []byte(name) {
		letter := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_'
		if !letter && (index == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}
