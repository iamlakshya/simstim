//go:build !linux

package host

import (
	"context"
	"github.com/iamlakshya/simstim/internal/proto"
	"net"
)

// WriteManagerStopRoleV1 has no native manager custody producer here.
func (*ManagerCustodyMainV1) WriteManagerStopRoleV1(context.Context, *OwnedSupervisorLease, net.Conn, proto.Response, uint64) error {
	return sessionLeaseError("native manager custody requires a Linux service main")
}

// AcceptManagerStopV1 has no native manager custody producer here.
func (*ManagerCustodyMainV1) AcceptManagerStopV1(context.Context, *OwnedSupervisorLease) error {
	return sessionLeaseError("native manager custody requires a Linux service main")
}
