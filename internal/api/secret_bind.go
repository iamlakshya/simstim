package api

// FeatureBoundSecrets must be checked before a bound value or binding form
// is sent. A legacy helper would otherwise expose the value to the guest.
const FeatureBoundSecrets = "bound-secrets-v1"

// SecretBind describes the named header of exact HTTPS destinations.
// It contains no value and grants no network access.
type SecretBind struct {
	Hosts  []string `json:"hosts"`
	Header string   `json:"header,omitempty"`
	Format string   `json:"format,omitempty"`
}

// AgentSecretBinding is public agent-table data. Store receives only the
// stand-in on standard input inside the wall, before the agent starts.
type AgentSecretBinding struct {
	Env    string   `json:"env"`
	Hosts  []string `json:"hosts"`
	Header string   `json:"header"`
	Format string   `json:"format"`
	Prefix string   `json:"prefix,omitempty"`
	Source string   `json:"source"`
	Store  []string `json:"store,omitempty"`
}

// CloneSecretBinds owns every destination slice before publication.
func CloneSecretBinds(input map[string]SecretBind) map[string]SecretBind {
	if input == nil {
		return nil
	}
	output := make(map[string]SecretBind, len(input))
	for name, binding := range input {
		binding.Hosts = append([]string(nil), binding.Hosts...)
		output[name] = binding
	}
	return output
}
