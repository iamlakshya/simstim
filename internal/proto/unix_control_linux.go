//go:build linux

package proto

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

const maxUnixControlDescriptorsV1 = 8

// EnableUnixCredentialsV1 enables kernel credentials before the first byte.
func EnableUnixCredentialsV1(conn *net.UnixConn) error {
	if conn == nil {
		return errors.New("native control has no original Unix connection")
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var optionErr error
	err = raw.Control(func(fd uintptr) {
		optionErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PASSCRED, 1)
	})
	return errors.Join(err, optionErr)
}

// ReceiveUnixControlV1 is the one bounded native ancillary decoder. It uses
// atomic MSG_CMSG_CLOEXEC and the connection's existing poll/deadline owner.
// Call it from the sole physical reader from byte zero; keep the ordinary
// protocol decoder and validate native descriptor roles at its actual consumer.
func ReceiveUnixControlV1(conn *net.UnixConn, data []byte, maxDescriptors int) (n int, control UnixControlV1, failure error) {
	if conn == nil || maxDescriptors < 0 || maxDescriptors > maxUnixControlDescriptorsV1 {
		return 0, control, errors.New("native control has no bounded original Unix receiver")
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, control, err
	}
	buffer := make([]byte, unix.CmsgSpace(maxDescriptors*4)+unix.CmsgSpace(unix.SizeofUcred))
	var oob, flags int
	var receiveErr error
	err = raw.Read(func(fd uintptr) bool {
		n, oob, flags, _, receiveErr = unix.Recvmsg(int(fd), data, buffer, unix.MSG_CMSG_CLOEXEC)
		return receiveErr != unix.EAGAIN && receiveErr != unix.EWOULDBLOCK
	})
	if n < 0 {
		n = 0
	}
	if oob < 0 || oob > len(buffer) {
		return n, control, errors.Join(err, receiveErr, errors.New("native control returned an invalid ancillary length"))
	}
	control, failure = decodeUnixControlV1(buffer[:oob])
	if flags&(unix.MSG_CTRUNC|unix.MSG_TRUNC) != 0 || len(control.Files) > maxDescriptors {
		failure = errors.Join(failure, errors.New("native control was truncated or exceeded its exact descriptor bound"))
	}
	failure = errors.Join(failure, err, receiveErr)
	if failure == nil && n > 0 {
		failure = newUnixReceiveWitnessV1(conn, &control)
	}
	if failure != nil {
		failure = errors.Join(failure, control.Close())
	}
	return n, control, failure
}

func decodeUnixControlV1(buffer []byte) (control UnixControlV1, failure error) {
	for len(buffer) >= unix.CmsgLen(0) {
		// This fixed native header comes from recvmsg, never a wire DTO. Clamp
		// a truncated last record before decoding its materialised FD prefix.
		header := *(*unix.Cmsghdr)(unsafe.Pointer(&buffer[0]))
		length := uint64(header.Len)
		if length < uint64(unix.CmsgLen(0)) {
			return control, errors.Join(failure, errors.New("native control has a malformed ancillary header"))
		}
		end := min(length, uint64(len(buffer)))
		payload := buffer[unix.CmsgLen(0):int(end)]
		switch {
		case header.Level == unix.SOL_SOCKET && header.Type == unix.SCM_RIGHTS:
			for len(payload) >= 4 {
				fd := int(int32(binary.NativeEndian.Uint32(payload[:4])))
				if fd < 0 {
					failure = errors.Join(failure, errors.New("native control returned an invalid descriptor"))
				} else {
					control.Files = append(control.Files, os.NewFile(uintptr(fd), "received-native-control"))
				}
				payload = payload[4:]
			}
			if len(payload) != 0 {
				failure = errors.Join(failure, errors.New("native control has a partial descriptor"))
			}
		case header.Level == unix.SOL_SOCKET && header.Type == unix.SCM_CREDENTIALS:
			message := unix.SocketControlMessage{Header: header, Data: payload}
			current, err := unix.ParseUnixCredentials(&message)
			if err != nil || control.Credentials != nil {
				failure = errors.Join(failure, errors.New("native control has ambiguous current sender credentials"), err)
			} else {
				control.Credentials = &UnixCredentialsV1{PID: int(current.Pid), UID: current.Uid, GID: current.Gid}
			}
		default:
			failure = errors.Join(failure, errors.New("native control has an unknown ancillary role"))
		}
		if length > uint64(len(buffer)) {
			return control, errors.Join(failure, errors.New("native control has an incomplete ancillary record"))
		}
		next := unix.CmsgSpace(int(length) - unix.CmsgLen(0))
		if next >= len(buffer) {
			return control, failure
		}
		buffer = buffer[next:]
	}
	return control, failure
}

// SendUnixControlV1 transfers this exact bounded descriptor vector once. A
// short stream write sends only remaining ordinary bytes, never more rights.
// The sender retains ownership of every supplied descriptor and closes its
// own loan copies after this call. It must serialise all connection writers.
func SendUnixControlV1(conn *net.UnixConn, data []byte, files []*os.File) error {
	if conn == nil || len(data) == 0 || len(files) > maxUnixControlDescriptorsV1 {
		return errors.New("native control has no bounded original Unix sender")
	}
	fds := make([]int, 0, len(files))
	for _, file := range files {
		if file == nil {
			return errors.New("native control is missing a supplied descriptor")
		}
		fds = append(fds, int(file.Fd()))
	}
	var rights []byte
	if len(fds) != 0 {
		rights = unix.UnixRights(fds...)
	}
	n, oob, err := conn.WriteMsgUnix(data, rights, nil)
	if err != nil {
		return err
	}
	if n == 0 || len(fds) != 0 && oob == 0 {
		return io.ErrShortWrite
	}
	for n < len(data) {
		written, err := conn.Write(data[n:])
		n += written
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
