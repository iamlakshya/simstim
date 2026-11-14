//go:build !linux

package supervisor

func (s *Supervisor) prepareStoppedVMResources() (sandbox, error) {
	return nil, execRefusal("VM resources require a Linux host")
}
