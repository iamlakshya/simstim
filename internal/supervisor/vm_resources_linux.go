//go:build linux

package supervisor

import "github.com/iamlakshya/simstim/internal/host"

func (s *Supervisor) prepareVMResources() (sandbox, error) {
	driver,err:=s.protectedVMDriver()
	if err!=nil{return nil,err}
	factory, err := host.MicroVMResourcesWithPublisherV1(driver,s.publishVMReferences)
	if err != nil {
		return nil, err
	}
	resource, err := factory.Build(s.lifetime)
	if err != nil {
		return nil, err
	}
	return wrapVMRuntimeV1(s.lifetime, resource)
}
