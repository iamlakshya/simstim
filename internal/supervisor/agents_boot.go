package supervisor

import (
	"context"
	"slices"

	"github.com/iamlakshya/simstim/internal/host"
)

// prepareBootAgents is reached only before the actual sandbox factory. Every
// authenticated client boot pins its exact protected selected agents bytes.
// Only the separately issued automatic operation can call the host producer.
func (s *Supervisor) prepareBootAgents(ctx context.Context) (*host.InstalledAgentsReceiptV1, error) {
	if s.m.Wall == nil || s.m.Wall.Driver != "chv" && s.m.Wall.Driver != "firecracker" && s.m.Wall.Driver != "nitro" {
		return nil, nil
	}
	if _, err := s.protectedVMDriver(); err != nil {
		return nil, err
	}
	if s.bootOperation == nil {
		return nil, execRefusal("the agents image has no original borrowed boot operation")
	}
	if authority := s.bootOperation.AutomaticBoot(); authority != nil {
		return host.PrepareAutomaticAgentsV1(ctx, authority, s.publishAutomaticAgents)
	}
	return host.PinOriginalBootAgentsV1(ctx)
}

func (s *Supervisor) publishAutomaticAgents(edit *host.AgentsRefreshV1) error {
	checked, err := edit.PublicationContextV1()
	if err != nil {
		return err
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if err := checked.Err(); err != nil {
		return err
	}
	var snapshot host.Manifest
	if err := s.writeManifestChange(&snapshot, false, nil, nil, edit.ApplyV1, func(saved *host.Manifest) {
		s.m.Agents = saved.Agents
		s.m.VMNeedV1 = slices.Clone(saved.VMNeedV1)
	}); err != nil {
		return err
	}
	if err := edit.CommitProtectedV1(checked); err != nil {
		return err
	}
	committed, err := edit.ExpectedContextV1()
	if err != nil {
		return err
	}
	s.lifetime = committed
	return nil
}
