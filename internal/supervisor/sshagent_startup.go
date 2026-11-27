package supervisor

import (
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
)

func (s *Supervisor) checkStartupNamedSSHKeys(root host.Manifest, message startupSecretEnvelope) error {
	var selected []string
	if root.Policy != nil {
		selected = slices.Clone(root.Policy.Sandbox.SSHKeys)
	}
	slices.Sort(selected)
	if message.NamedSSHKeys == nil {
		if len(selected) > 0 && !message.AllowMissing {
			return namedSSHError("the selected cold-start SSH keys were not delivered")
		}
		return nil
	}
	keys := message.NamedSSHKeys
	if !keys.Valid() || !slices.Equal(keys.Names, selected) || s.originalStart == nil || s.bootOperation == nil || !s.bootOperation.Valid(s.id) || root.Wall == nil || root.Wall.Driver == "plain" {
		return namedSSHError("the SSH keys have no matching original native cold-start source")
	}
	for _, name := range keys.Names {
		if _, ordinary := message.Env[name]; ordinary {
			return namedSSHError("an SSH key cannot enter ordinary secret delivery")
		}
	}
	return nil
}

func (s *Supervisor) startPendingNamedSSHKeys() error {
	s.mu.Lock()
	keys := s.startupNamedSSHKeys
	s.startupNamedSSHKeys = nil
	lifetime := s.lifetime
	s.mu.Unlock()
	if keys == nil {
		return nil
	}
	defer clear(keys.Values)
	if !keys.Valid() {
		return namedSSHError("the original cold-start SSH key delivery is invalid")
	}
	return s.startNamedSSHAgent(lifetime, keys.Names, keys.Values)
}

func (s *Supervisor) clearPendingNamedSSHKeys() {
	s.mu.Lock()
	if s.startupNamedSSHKeys != nil {
		clear(s.startupNamedSSHKeys.Values)
		s.startupNamedSSHKeys = nil
	}
	s.mu.Unlock()
}

func cloneStartupNamedSSHKeys(keys *api.NamedSSHKeysV1) *api.NamedSSHKeysV1 {
	return api.CloneNamedSSHKeysV1(keys)
}
