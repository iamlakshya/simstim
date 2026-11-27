//go:build !linux

package host

import (
	"context"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

type nativeStartupPeerV1 struct{}
type pendingStartupV1 struct {
	owner *OwnedSupervisorLease
	line  *StartupLineV1
	peer  *nativeStartupPeerV1
	nonce string
}

func enableStartupAncillary(net.Conn) error {
	return sessionLeaseError("native sandbox startup requires a supported Linux host")
}
func receiveStartupAncillaryBoundV1(net.Conn, []byte, int) (int, []*os.File, *startupCredentials, error) {
	return 0, nil, nil, sessionLeaseError("native auxiliary startup requires a supported Linux host")
}
func receiveStartupAncillary(net.Conn, []byte) (int, []*os.File, *startupCredentials, error) {
	return 0, nil, nil, sessionLeaseError("native sandbox startup requires a supported Linux host")
}
func sendStartupAncillary(net.Conn, proto.Type, []byte, *os.File) error {
	return sessionLeaseError("native sandbox startup requires a supported Linux host")
}
func (owner *OwnedSupervisorLease) PrepareStartupHello(context.Context, *StartupLineV1) (string, error) {
	return "", sessionLeaseError("native sandbox startup requires a supported Linux host")
}
func (peer *nativeStartupPeerV1) CheckSender(startupCredentials) error {
	return sessionLeaseError("native sandbox startup requires a supported Linux host")
}
func (peer *nativeStartupPeerV1) Check() error {
	return sessionLeaseError("native sandbox startup requires a supported Linux host")
}
func (peer *nativeStartupPeerV1) Close() error { return nil }

func checkBorrowedFlockV1(*os.File, startupCredentials) error {
	return sessionLeaseError("native sandbox startup requires a supported Linux host")
}

type startupLaunchV1 struct{}

func (launch *startupLaunchV1) Close() error { return nil }
func startOwnedReservedSession(*Manifest, map[string]api.Secret, *sessionReservation, *SessionOperation) (*SessionInfo, error) {
	return nil, UserError("wall_unavailable", "native sandbox startup requires a supported Linux host", "Choose a supported Linux host.")
}

func startOwnedSessionOperation(context.Context, *SessionOperation, map[string]api.Secret, []api.MemberResumeArgs, bool, *api.SecretDeliveryIntent) (*Manifest, *startupLaunchV1, error) {
	return nil, nil, UserError("wall_unavailable", "native sandbox startup requires a supported Linux host", "Choose a supported Linux host.")
}
