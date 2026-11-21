//go:build linux

package host

import (
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/proto"
)

func enableStartupAncillary(conn net.Conn) error {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return sessionLeaseError("startup control is not the original Unix connection")
	}
	return proto.EnableUnixCredentialsV1(unixConn)
}

func receiveStartupAncillary(conn net.Conn, data []byte) (int, []*os.File, *startupCredentials, error) {
	return receiveStartupAncillaryBoundV1(conn, data, 1)
}

func receiveStartupAncillaryBoundV1(conn net.Conn, data []byte, roles int) (int, []*os.File, *startupCredentials, error) {
	if roles != 1 && roles != 3 {
		return 0, nil, nil, sessionLeaseError("startup native role bound is not a closed supported purpose")
	}
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, nil, nil, sessionLeaseError("startup lost its native Unix connection")
	}
	n, control, err := proto.ReceiveUnixControlV1(unixConn, data, roles)
	var cred *startupCredentials
	if control.Credentials != nil {
		cred = &startupCredentials{pid: control.Credentials.PID, uid: control.Credentials.UID, gid: control.Credentials.GID}
	}
	if err != nil {
		return n, nil, cred, err
	}
	files, detachErr := control.DetachFilesV1()
	if detachErr != nil {
		control.Close()
		return n, nil, cred, detachErr
	}
	return n, files, cred, nil
}

func sendStartupAncillary(conn net.Conn, kind proto.Type, payload []byte, file *os.File) error {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok || file == nil {
		return sessionLeaseError("the loan lost its original native connection or descriptor")
	}
	return proto.SendUnixFrameV1(unixConn, kind, payload, []*os.File{file})
}
