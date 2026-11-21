//go:build !linux

package gvisor

import (
	"context"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/wall"
)

type LiveOwnerV1 struct{}

func NativeLiveOwnerV1(wall.Sandbox, net.Conn) (*LiveOwnerV1, error) { return nil, nil }
func (*LiveOwnerV1) Check(context.Context) error {
	return failure("wall_setup", "gvisor native continuation needs Linux", nil)
}

// FreezeV1 needs the actual original Linux cgroup creator.
func (*LiveOwnerV1) FreezeV1(context.Context, int) error {
	return failure("freeze_off", "gvisor freezing needs the original Linux cgroup creator", nil)
}

// ThawV1 has no native freeze to release on another host.
func (*LiveOwnerV1) ThawV1() error { return nil }

// FreezeAvailableV1 refuses unavailable native support.
func (*LiveOwnerV1) FreezeAvailableV1() bool { return false }

// FreezeReasonV1 describes the unavailable native capability.
func (*LiveOwnerV1) FreezeReasonV1() string { return "gvisor freezing needs Linux" }

// WithForkRootsV1 needs the original native Linux mounted-root issuer.
func (*LiveOwnerV1) WithForkRootsV1(context.Context, func(*os.Root, *os.Root) error) error {
	return failure("fork_off", "gvisor fork roots need the original Linux creator", nil)
}
