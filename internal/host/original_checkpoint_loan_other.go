//go:build !linux

package host

import (
	"context"
	"github.com/iamlakshya/simstim/internal/proto"
	"net"
)

func (*OriginalVMNeed) SendCheckpointLoanV1(context.Context, net.Conn, *OwnedSupervisorLease, *SessionOperation, *StartupLineV1, proto.Response) (bool, error) {
	return false, vmNeedError("native Snapshot loans require Linux")
}

func (*OriginalVMNeed) SendCheckpointReservationLoanV2(context.Context, net.Conn, *OwnedSupervisorLease, *SessionOperation, *StartupLineV1, proto.Response) (bool, error) {
	return false, vmNeedError("reserved native Snapshot loans require Linux")
}
