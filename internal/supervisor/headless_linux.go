//go:build linux

package supervisor

import (
	"context"
	"slices"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/wall"
)

func (s *Supervisor) prepareStoppedVMResources() (sandbox, error) {
	// Existing protected references must already cover this recorded Need.
	// The operation may not publish a new ordinary session/Life record.
	factory, err := host.CloudHypervisorResourcesWithPublisherV1(func(ctx context.Context, spec wall.Spec) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		checked := host.CloneManifest(*s.m)
		if err := host.SetVMReferences(&checked, spec); err != nil {
			return err
		}
		if checked.Run.Image != s.m.Run.Image || checked.Run.Label != s.m.Run.Label || !slices.Equal(checked.Run.Artefacts, s.m.Run.Artefacts) {
			return execRefusal("the stopped VM's protected references need an admitted resume before export")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	resource, err := factory.Build(s.lifetime)
	if err != nil {
		return nil, err
	}
	return wrapVMRuntimeV1(s.lifetime, resource)
}
