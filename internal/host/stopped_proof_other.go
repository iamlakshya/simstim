//go:build !linux

package host

import (
	"context"
	"github.com/iamlakshya/simstim/internal/proto"
	"net"
)

func (*OwnedSupervisorLease) WriteNativeStopProofV1(context.Context, net.Conn, proto.Response) error {
	return sessionLeaseError("a whole-unit native stop proof requires a supported Linux host")
}
