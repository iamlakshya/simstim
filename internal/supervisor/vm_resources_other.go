//go:build !linux

package supervisor

import "github.com/iamlakshya/simstim/internal/proto"

func (*Supervisor) prepareVMResources() (sandbox, error) {
	return nil, &proto.Error{Code: "wall_unavailable", Message: "Cloud Hypervisor requires a Linux host", Hint: "Choose a supported Linux host for this microvm."}
}
