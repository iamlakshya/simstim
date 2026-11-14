package host

import (
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
)

// withSSHAgentSocket removes inherited forwarding and installs only the
// stable per-session path. A connection's temporary socket is never saved.
func withSSHAgentSocket(env []string, id string, enabled bool) ([]string, error) {
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, "SSH_AUTH_SOCK=") {
			out = append(out, entry)
		}
	}
	if !enabled {
		return out, nil
	}
	if !api.ValidSessionID(id) {
		return nil, agentSocketError()
	}
	r, err := paths.OpenPrivateRuntimeDir()
	if err != nil {
		return nil, agentSocketError()
	}
	if err := r.Close(); err != nil {
		return nil, agentSocketError()
	}
	path := paths.AgentSocket(id)
	if path == "" || len(path) > paths.MaxUnixSocketPath {
		return nil, agentSocketError()
	}
	return append(out, "SSH_AUTH_SOCK="+path), nil
}
