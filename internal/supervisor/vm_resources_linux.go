//go:build linux

package supervisor

import "github.com/iamlakshya/simstim/internal/host"

func (s *Supervisor) prepareVMResources() (sandbox, error) {
	factory, err := host.CloudHypervisorResourcesWithPublisherV1(s.publishVMReferences)
	if err != nil {
		return nil, err
	}
	resource, err := factory.Build(s.lifetime)
	if err != nil {
		return nil, err
	}
	return wrapVMRuntimeV1(s.lifetime, resource)
}
