//go:build !linux

package host

import (
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/proto"
)

func receiveStartupAncillaryRetainedBoundV4(net.Conn, []byte, int, *proto.UnixControlRetentionV1) (int, []*os.File, *startupCredentials, error) {
	return 0, nil, nil, sessionLeaseError("native resource retirement receive requires Linux")
}

func receiveSlotSnapshotRetainedV4(net.Conn, []byte, *proto.UnixControlRetentionV1) (int, []*os.File, *startupCredentials, error) {
	return 0, nil, nil, sessionLeaseError("native resource retirement receive requires Linux")
}

func receiveSlotSnapshotReservationRetainedV4(net.Conn, []byte, *proto.UnixControlRetentionV1) (int, []*os.File, *startupCredentials, error) {
	return 0, nil, nil, sessionLeaseError("native resource retirement receive requires Linux")
}

func receiveSlotReservedStartupRetainedV4(net.Conn, []byte, *proto.UnixControlRetentionV1) (int, []*os.File, *startupCredentials, error) {
	return 0, nil, nil, sessionLeaseError("native resource retirement receive requires Linux")
}
