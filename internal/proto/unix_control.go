package proto

import (
	"errors"
	"net"
	"os"
)

// UnixCredentialsV1 is a kernel observation from one native receive. These
// values are facts only; their numbers never issue an execution or stop grant.
type UnixCredentialsV1 struct {
	PID int
	UID uint32
	GID uint32
}

// UnixControlV1 owns every descriptor materialised by one native receive.
// The receiver must validate exact roles and frame association before transfer.
// Legacy receive errors close all descriptors. The retained variant instead
// keeps actual failed objects for its original resource owner's first Close.
type UnixControlV1 struct {
	Files       []*os.File
	Credentials *UnixCredentialsV1
	received    *unixReceiveWitnessV1
}

// Close closes every still-owned descriptor, including an unclaimed prefix.
func (control *UnixControlV1) Close() error {
	if control == nil {
		return nil
	}
	if control.received != nil {
		failure := control.received.close()
		control.Files, control.Credentials = nil, nil
		return failure
	}
	var failure error
	for _, file := range control.Files {
		if file != nil {
			failure = errors.Join(failure, file.Close())
		}
	}
	control.Files, control.Credentials = nil, nil
	return failure
}

// SendUnixFrameV1 uses the canonical frame encoder on an already owned native
// Unix connection. The native sender transfers this descriptor vector once.
func SendUnixFrameV1(conn *net.UnixConn, kind Type, payload []byte, files []*os.File) error {
	raw, err := frame(kind, payload)
	if err != nil {
		return err
	}
	return SendUnixControlV1(conn, raw, files)
}
