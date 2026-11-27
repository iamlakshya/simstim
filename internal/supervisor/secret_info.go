package supervisor

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
)

// snapshotForInfo preserves the selected identity while projecting the
// CURRENT root CAS. This public view never supplies local fetch authority.
// The caller can slim the public envelope for an ordinary summary later.
func (s *Supervisor) snapshotForInfo() (host.Manifest, error) {
	s.mu.Lock()
	if s.m == nil {
		s.mu.Unlock()
		return host.Manifest{}, secretIntentFailure()
	}
	selected := s.snapshot()
	s.mu.Unlock()
	if s.unwalledV1() {
		selected.DebugUntil = s.noneDebugUntilV1()
	}
	root := s.owner()
	if root == s {
		return selected, nil
	}
	root.mu.Lock()
	if root.m == nil {
		root.mu.Unlock()
		return host.Manifest{}, secretIntentFailure()
	}
	current := api.CloneSessionSecretState(root.m.SecretState)
	root.mu.Unlock()
	if current == nil && selected.SecretState != nil {
		return host.Manifest{}, secretIntentFailure()
	}
	selected.SecretState = current
	return selected, nil
}
