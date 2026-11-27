package host

import (
	"context"

	"github.com/iamlakshya/simstim/internal/agentbridge"
)

func prepareHostAgentEndpoint(_ agentbridge.Scope) (*hostAgentEndpoint, error) {
	return nil, UserError("ssh_agent_unsupported", "this host cannot create a private SSH agent endpoint",
		"Use a macOS or Linux host, or use a dedicated deploy key.")
}

func prepareHostAgentEndpointContext(_ context.Context, scope agentbridge.Scope) (*hostAgentEndpoint, error) {
	return prepareHostAgentEndpoint(scope)
}
