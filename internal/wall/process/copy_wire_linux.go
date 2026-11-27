//go:build linux

package process

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

const copyRootIndex = ^uint32(0)
const copyEndIndex = copyRootIndex - 1

// copyMessage consumes ancillary data only on this factory-created private
// line. Its caller supplies the exact data and FD counts for the current role.
func copyMessage(conn *net.UnixConn, size, maxFDs int) ([]byte, []*os.File, error) {
	data := make([]byte, size)
	n, control, err := proto.ReceiveUnixControlV1(conn, data, maxFDs)
	if err == nil && (n == 0 || control.Credentials != nil) {
		err = fmt.Errorf("invalid original process copy control")
	}
	if err == nil && n < size {
		err = copyOrdinaryRead(conn, data[n:])
	}
	if err != nil {
		return nil, nil, errors.Join(err, control.Close())
	}
	return data, control.Files, nil
}
func copyOrdinaryRead(conn *net.UnixConn, data []byte) error {
	for len(data) > 0 {
		n, control, err := proto.ReceiveUnixControlV1(conn, data, 0)
		if err == nil && (n == 0 || control.Credentials != nil || len(control.Files) != 0) {
			err = fmt.Errorf("unexpected original copy ancillary data or EOF")
		}
		closeErr := control.Close()
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
		data = data[n:]
	}
	return nil
}
func copyWrite(conn *net.UnixConn, data []byte) error {
	for len(data) > 0 {
		n, err := conn.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
func copyStatus(conn *net.UnixConn) error {
	data, files, err := copyMessage(conn, 1, 0)
	if err != nil {
		return err
	}
	if len(files) != 0 || data[0] != 'A' {
		return fmt.Errorf("invalid original copy completion")
	}
	var code [4]byte
	if err = copyOrdinaryRead(conn, code[:]); err != nil {
		return err
	}
	if number := binary.LittleEndian.Uint32(code[:]); number != 0 {
		return unix.Errno(number)
	}
	return nil
}
