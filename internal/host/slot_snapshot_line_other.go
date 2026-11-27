//go:build !linux

package host

import (
	"net"
	"os"
)

func receiveSlotSnapshotAncillaryV1(net.Conn, []byte) (int, []*os.File, *startupCredentials, error) {
	return 0, nil, nil, sessionLeaseError("native Snapshot transfer requires Linux")
}

func receiveSlotSnapshotReservationAncillaryV2(net.Conn, []byte) (int, []*os.File, *startupCredentials, error) {
	return 0, nil, nil, sessionLeaseError("reserved native Snapshot transfer requires Linux")
}
