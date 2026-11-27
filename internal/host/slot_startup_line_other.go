//go:build !linux

package host

import (
	"context"
	"github.com/iamlakshya/simstim/internal/proto"
	"net"
	"os"
)

func receiveSlotReservedStartupAncillaryV2(net.Conn, []byte) (int, []*os.File, *startupCredentials, error) {
	return 0, nil, nil, sessionLeaseError("reserved native startup requires Linux")
}
func (*OwnedSupervisorLease) PrepareSlotReservedStartupV2(context.Context, *StartupLineV1, proto.Frame) error {
	return sessionLeaseError("reserved native startup requires Linux")
}
func (*OwnedSupervisorLease) AcceptSlotReservedStartupV2(context.Context, *StartupLineV1, proto.Frame) (*SessionOperation, error) {
	return nil, sessionLeaseError("reserved native startup requires Linux")
}
