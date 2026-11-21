//go:build !linux && !darwin

package none

import (
	"context"
	"net"

	"github.com/iamlakshya/simstim/internal/wall"
)

func newSandbox(context.Context, wall.Spec) (wall.Sandbox, error) {
	return nil, failure("this platform has no host helper process runtime", nil)
}

type LiveOwnerV1 struct{}

func (*LiveOwnerV1) CleanupObservationV1() (int, string, error) {
	return 0, "", failure("this platform has no host helper process runtime", nil)
}

func NativeLiveOwnerV1(wall.Sandbox, net.Conn) (*LiveOwnerV1, error) {
	return nil, failure("this platform has no host helper process runtime", nil)
}
func (*LiveOwnerV1) Check(context.Context) error {
	return failure("this platform has no host helper process runtime", nil)
}

func (*LiveOwnerV1) CheckSessionV1(context.Context, string, net.Conn) error {
	return failure("this platform has no host helper process runtime", nil)
}
