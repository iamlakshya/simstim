//go:build !linux

package jail

import (
	"context"

	"github.com/iamlakshya/simstim/internal/proto"
)

// FreezeAllProcessesV1 requires the actual original Linux cgroup creator.
func (*Cmd) FreezeAllProcessesV1(context.Context, int) error {
	return &proto.Error{Code: "freeze_off", Message: "gvisor fork freezing needs its original Linux cgroup creator", Hint: "Use a gvisor sandbox started by the current helper in its delegated user unit."}
}

// ThawAllProcessesV1 has no native freeze to release outside Linux.
func (*Cmd) ThawAllProcessesV1() error { return nil }

// FreezeAvailableV1 cannot report native Linux support on another host.
func (*Cmd) FreezeAvailableV1() bool { return false }

// FreezeReasonV1 reports the unavailable native capability.
func (*Cmd) FreezeReasonV1() string { return "gvisor fork freezing needs Linux" }
