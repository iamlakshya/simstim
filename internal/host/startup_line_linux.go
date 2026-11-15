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
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, nil, nil, sessionLeaseError("startup lost its native Unix connection")
	}
	n, control, err := proto.ReceiveUnixControlV1(unixConn, data, 1)
	var cred *startupCredentials
	if control.Credentials != nil {
		cred = &startupCredentials{pid: control.Credentials.PID, uid: control.Credentials.UID, gid: control.Credentials.GID}
	}
	return n, control.Files, cred, err
}

func sendStartupAncillary(conn net.Conn, kind proto.Type, payload []byte, file *os.File) error {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok || file == nil {
		return sessionLeaseError("the loan lost its original native connection or descriptor")
	}
	return proto.SendUnixFrameV1(unixConn, kind, payload, []*os.File{file})
}
