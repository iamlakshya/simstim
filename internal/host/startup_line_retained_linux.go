//go:build linux

package host

import (
	"errors"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/proto"
)

func receiveStartupAncillaryRetainedBoundV4(conn net.Conn, data []byte, roles int, retention *proto.UnixControlRetentionV1) (int, []*os.File, *startupCredentials, error) {
	if roles != 1 && roles != 3 {
		return 0, nil, nil, sessionLeaseError("startup native role bound is not a closed supported purpose")
	}
	return receiveStartupAncillaryRetainedExactV4(conn, data, roles, retention)
}

func receiveSlotSnapshotRetainedV4(conn net.Conn, data []byte, retention *proto.UnixControlRetentionV1) (int, []*os.File, *startupCredentials, error) {
	return receiveStartupAncillaryRetainedExactV4(conn, data, proto.NativeCheckpointRoleCountV1, retention)
}

func receiveSlotSnapshotReservationRetainedV4(conn net.Conn, data []byte, retention *proto.UnixControlRetentionV1) (int, []*os.File, *startupCredentials, error) {
	return receiveStartupAncillaryRetainedExactV4(conn, data, proto.NativeCheckpointReservationRoleCountV2, retention)
}

func receiveSlotReservedStartupRetainedV4(conn net.Conn, data []byte, retention *proto.UnixControlRetentionV1) (int, []*os.File, *startupCredentials, error) {
	return receiveStartupAncillaryRetainedExactV4(conn, data, proto.NativeSlotReservedStartupRoleCountV2, retention)
}

func receiveStartupAncillaryRetainedExactV4(conn net.Conn, data []byte, roles int, retention *proto.UnixControlRetentionV1) (int, []*os.File, *startupCredentials, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, nil, nil, sessionLeaseError("startup lost its native Unix connection")
	}
	n, control, err := proto.ReceiveUnixControlRetainedV1(unixConn, data, roles, retention)
	var cred *startupCredentials
	if control.Credentials != nil {
		cred = &startupCredentials{pid: control.Credentials.PID, uid: control.Credentials.UID, gid: control.Credentials.GID}
	}
	if err != nil {
		return n, control.Files, cred, err
	}
	files, detached := control.DetachFilesV1()
	if detached != nil {
		// Detach can fail after the receive witness was created. Keep every
		// actual object visible to this same owner rather than closing here.
		return n, control.Files, cred, errors.Join(err, detached)
	}
	return n, files, cred, nil
}
