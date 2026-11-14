package supervisor

import (
	"context"
	"slices"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/wall"
)

// prepareBootAgents is reached only before the actual sandbox factory. Every
// authenticated client boot pins its exact protected selected agents bytes.
// Only the separately issued automatic operation can call the host producer.
func (s *Supervisor) prepareBootAgents(ctx context.Context) (*host.InstalledAgentsReceiptV1, error) {
	if s.m.Wall == nil || s.m.Wall.Driver != "chv" {
		return nil, nil
	}
	if authority := s.bootOperation.AutomaticBoot(); authority != nil {
		return host.PrepareAutomaticAgentsV1(ctx, authority, s.publishAutomaticAgents)
	}
	spec, ok := wall.ExpectedSpec(ctx)
	if !ok || spec.VM == nil || spec.ID != s.id || s.m.Agents != "" && s.m.Agents != spec.VM.Agents.SHA256 {
		return nil, execRefusal("the exact client agents selection differs from its protected boot plan")
	}
	return host.PinInstalledAgentsV1(ctx, spec.VM.Agents)
}

func (s *Supervisor) publishAutomaticAgents(edit *host.AgentsRefreshV1) error {
	checked, err := edit.ExpectedContextV1()
	if err != nil {
		return err
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if err := checked.Err(); err != nil {
		return err
	}
	var snapshot host.Manifest
	return s.writeManifestChange(&snapshot, false, nil, nil, edit.ApplyV1, func(saved *host.Manifest) {
		s.m.Agents = saved.Agents
		s.m.VMNeedV1 = slices.Clone(saved.VMNeedV1)
		s.lifetime = checked
	})
}
