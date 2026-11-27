package agents

import (
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

// SecretBinding is the shared public binding schema, never a secret value.
type SecretBinding = api.AgentSecretBinding

// These first-class bindings come from isolation spec 7.3 and 8.6. The
// OAuth header and prefix checks named there still need the owner's manual
// recorded-traffic check. Other agents with an unverified header stay empty.
var secretData = map[string][]SecretBinding{
	"claude": {
		{Env: "ANTHROPIC_API_KEY", Hosts: []string{"api.anthropic.com"}, Header: "x-api-key", Format: "{value}", Prefix: "sk-ant-api03-", Source: "https://code.claude.com/docs/en/authentication"},
		{Env: "ANTHROPIC_AUTH_TOKEN", Hosts: []string{"api.anthropic.com"}, Header: "Authorization", Format: "Bearer {value}", Source: "https://code.claude.com/docs/en/authentication"},
		{Env: "CLAUDE_CODE_OAUTH_TOKEN", Hosts: []string{"api.anthropic.com"}, Header: "Authorization", Format: "Bearer {value}", Prefix: "sk-ant-oat01-", Source: "https://code.claude.com/docs/en/authentication"},
	},
	"codex": {
		{Env: "OPENAI_API_KEY", Hosts: []string{"api.openai.com"}, Header: "Authorization", Format: "Bearer {value}", Store: []string{"codex", "login", "--with-api-key"}, Source: "https://developers.openai.com/codex/auth"},
		{Env: "CODEX_API_KEY", Hosts: []string{"api.openai.com"}, Header: "Authorization", Format: "Bearer {value}", Source: "https://developers.openai.com/codex/auth"},
	},
}

func withSecretData(table []Agent) []Agent {
	for i := range table {
		table[i].Secrets = cloneSecretBindings(secretData[table[i].Kind])
	}
	return table
}

// SecretBindings returns owned data for one prospective registry or member.
func (agent Agent) SecretBindings() []SecretBinding {
	return cloneSecretBindings(agent.Secrets)
}

func cloneSecretBindings(input []SecretBinding) []SecretBinding {
	output := slices.Clone(input)
	for i := range output {
		output[i].Hosts = slices.Clone(output[i].Hosts)
		output[i].Store = slices.Clone(output[i].Store)
	}
	return output
}
