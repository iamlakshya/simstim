package api

import (
	"maps"

	"github.com/iamlakshya/simstim/internal/brand"
)

// SessionIdentity is trusted start metadata, never a user environment layer.
// Host is the originally selected saved host label. Sandbox is the primary
// session's first name, even after a rename or when starting another member.
type SessionIdentity struct {
	Session string
	Host    string
	Agent   string
	Sandbox string
}

// ApplySessionIdentity applies the four protected variables after user layers.
// It preserves the existing SESSION meaning: the individual session ID.
// It does not supply a root helper, Git or holder environment.
func ApplySessionIdentity(user map[string]string, identity SessionIdentity) map[string]string {
	result := maps.Clone(user)
	if result == nil {
		result = map[string]string{}
	}
	result[brand.Env("SESSION")] = identity.Session
	result[brand.Env("HOST")] = identity.Host
	result[brand.Env("AGENT")] = identity.Agent
	result[brand.Env("SANDBOX")] = identity.Sandbox
	return result
}
