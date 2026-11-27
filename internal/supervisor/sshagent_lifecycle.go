package supervisor

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// startNamedSSHAgent consumes only the separate original named-key grant.
// Ordinary secret delivery, policy names and guest bits never supply values.
func (s *Supervisor) startNamedSSHAgent(ctx context.Context, names []string, values map[string]api.Secret) error {
	if len(names) == 0 && len(values) == 0 {
		return nil
	}
	agent, err := newNamedSSHAgent(ctx, names, values, s.audit)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.sshAgent != nil || s.sshAgentStopped || s.egressStopping || s.lifetime == nil || s.lifetime.Err() != nil {
		s.mu.Unlock()
		agent.close()
		return namedSSHError("the original SSH key grant has no active supervisor")
	}
	s.sshAgent = agent
	s.mu.Unlock()
	return nil
}

func (s *Supervisor) namedSSHAgentEnabled() bool {
	return len(s.namedSSHKeyNames()) > 0
}

func (s *Supervisor) requireNamedSSHGuest(ctx context.Context) error {
	if !s.namedSSHAgentEnabled() {
		return nil
	}
	var raw json.RawMessage
	if err := s.ctl.request(ctx, proto.CtlGuestCapabilities, struct{}{}, &raw); err != nil {
		return err
	}
	var result proto.CtlGuestFeatures
	if !api.ExactJSONFields(raw, result) || json.Unmarshal(raw, &result) != nil || result.Version != 1 || !slices.Contains(result.Features, api.FeatureNamedSSHKeysV1) {
		return &proto.Error{Code: "wall_helper_old", Message: "the guest helper cannot serve the selected named SSH keys", Hint: "Update the host runtime and guest tools, then start the sandbox again."}
	}
	return nil
}

func (s *Supervisor) admitNamedSSHAgent(stream *link.Stream) *namedSSHAgent {
	var header json.RawMessage
	if stream.Header(&header) != nil {
		_ = stream.Close()
		return nil
	}
	var parsed struct {
		Kind string `json:"kind"`
	}
	fields, err := api.JSONObjectFields(header, "kind")
	if err != nil || len(fields) != 1 || !api.ExactJSONFields(header, parsed) || json.Unmarshal(header, &parsed) != nil || parsed.Kind != string(link.KindAgent) {
		_ = stream.Close()
		return nil
	}
	s.mu.Lock()
	agent := s.sshAgent
	s.mu.Unlock()
	// Admission happens before spawning. The agent's own lock bounds all
	// active handlers and prevents a close from racing a new WaitGroup Add.
	if !agent.admit(stream) {
		return nil
	}
	return agent
}

func (s *Supervisor) stopNamedSSHAgent() {
	s.mu.Lock()
	s.sshAgentStopped = true
	agent := s.sshAgent
	s.mu.Unlock()
	agent.close()
}
