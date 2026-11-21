//go:build !linux

package host

import (
	"context"
	"net"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall/none"
)

func (*OwnedSupervisorLease) WriteNativeNoneWarmProofV1(context.Context, net.Conn, proto.Response, *none.LiveOwnerV1) error {
	return sessionLeaseError("the native unwalled warm proof needs a supported Linux host")
}
