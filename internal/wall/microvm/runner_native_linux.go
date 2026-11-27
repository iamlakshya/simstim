//go:build linux

package microvm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall"
	"golang.org/x/sys/unix"
)

// nativeLineReader feeds the existing JSON-line decoder exactly one byte at a
// time. It never reads across a line boundary before that line's ancillary
// roles have been consumed. This is private control, never terminal traffic.
type nativeLineReader struct {
	connection *net.UnixConn
	control    proto.UnixControlV1
	offset     int
	modeMu     sync.Mutex
	mode       *vmmExecPhaseV1
}

func (r *nativeLineReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	n, current, err := proto.ReceiveUnixControlV1(r.connection, buffer[:1], 2)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		current.Close()
		return 0, io.EOF
	}
	if current.Credentials == nil || current.Credentials.PID <= 0 {
		current.Close()
		return 0, fmt.Errorf("private VMM control lacks its native sender credentials")
	}
	if current.Credentials.UID != uint32(os.Geteuid()) {
		r.modeMu.Lock()
		phase := r.mode
		r.modeMu.Unlock()
		if phase == nil || phase.command.CheckVMMNativeSenderV1(phase.source, r.connection, current.Credentials) != nil {
			current.Close()
			return 0, fmt.Errorf("private VMM control sender has no original native mapped identity")
		}
	}
	if r.control.Credentials != nil && *r.control.Credentials != *current.Credentials {
		current.Close()
		return 0, fmt.Errorf("private VMM control changed sender within a frame")
	}
	if len(current.Files) != 0 && (r.offset != 0 || len(r.control.Files) != 0) {
		current.Close()
		return 0, fmt.Errorf("private VMM descriptors are not on their frame's first byte")
	}
	if r.offset == 0 {
		r.control = current // preserve the actual FIRST native receive witness
	} else {
		current.Close() // no later frame byte may carry a descriptor role
	}
	r.offset += n
	return n, nil
}

func (r *nativeLineReader) takeControl() proto.UnixControlV1 {
	result := r.control
	r.control, r.offset = proto.UnixControlV1{}, 0
	return result
}

func (r *nativeLineReader) Close() error { return r.control.Close() }

func newNativeRunnerControl(connection net.Conn, spec wall.Spec) (*runnerControl, error) {
	local, ok := connection.(*net.UnixConn)
	if !ok {
		return nil, fmt.Errorf("the owned VMM control needs its original Unix socket pair")
	}
	if err := proto.EnableUnixCredentialsV1(local); err != nil {
		return nil, err
	}
	proof := &receivedVMMProof{session: spec.ID, spec: cloneBindingSpec(spec)}
	reader := &nativeLineReader{connection: local}
	return startRunnerControl(connection, reader, reader, proof), nil
}

func duplicateNativeFile(source interface {
	SyscallConn() (syscall.RawConn, error)
}, name string) (*os.File, error) {
	raw, err := source.SyscallConn()
	if err != nil {
		return nil, err
	}
	fd := -1
	var duplicateErr error
	err = raw.Control(func(current uintptr) { fd, duplicateErr = unix.FcntlInt(current, unix.F_DUPFD_CLOEXEC, 0) })
	if err != nil || duplicateErr != nil {
		if fd >= 0 {
			unix.Close(fd)
		}
		return nil, errors.Join(err, duplicateErr)
	}
	return os.NewFile(uintptr(fd), name), nil
}

func (s *runnerServer) sendNativeReady(session, driver string, owner *ownedVMMEndpoint, guest net.Conn) error {
	control, ok := s.conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("the VMM native-ready sender lost its original Unix channel")
	}
	buffered, ok := guest.(*bufferedVsock)
	if !ok {
		return fmt.Errorf("the VMM native-ready sender has another guest stream")
	}
	local, ok := buffered.Conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("the VMM guest stream has no original native Unix endpoint")
	}
	owner.mu.Lock()
	fd := -1
	var err error
	if owner.fd >= 0 {
		fd, err = unix.FcntlInt(uintptr(owner.fd), unix.F_DUPFD_CLOEXEC, 0)
	} else {
		err = fmt.Errorf("the original VMM owner is closed")
	}
	owner.mu.Unlock()
	if err != nil {
		return err
	}
	process := os.NewFile(uintptr(fd), "original-vmm-pidfd-loan")
	defer process.Close()
	peer, err := duplicateNativeFile(local, "original-vmm-guest-peer")
	if err != nil {
		return err
	}
	defer peer.Close()
	if err := owner.live(); err != nil {
		return err
	}
	data, err := json.Marshal(runnerMessage{Kind: "ready", Session: session, Driver: driver})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return proto.SendUnixControlV1(control, append(data, '\n'), []*os.File{process, peer})
}

// The original trusted runner transfers ONLY the handle created by THIS
// command.SysProcAttr.PidFD. No advertised PID/pidfd_open adopts another child.
// Actual parent Cmd/source ancestry and code are checked by the receiver.
func (s *runnerServer) sendNativeModeExecChild(session, driver string, owner *ownedVMMEndpoint) error {
	control, ok := s.conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("VMM final child lost its original native control")
	}
	owner.mu.Lock()
	fd := -1
	var err error
	if owner.fd >= 0 {
		fd, err = unix.FcntlInt(uintptr(owner.fd), unix.F_DUPFD_CLOEXEC, 0)
	} else {
		err = fmt.Errorf("original final VMM creation owner is closed")
	}
	owner.mu.Unlock()
	if err != nil {
		return err
	}
	process := os.NewFile(uintptr(fd), "original-final-helper-creation-loan")
	defer process.Close()
	if err := owner.live(); err != nil {
		return err
	}
	data, err := json.Marshal(runnerMessage{Kind: "mode_exec_child", Session: session, Driver: driver})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return proto.SendUnixControlV1(control, append(data, '\n'), []*os.File{process})
}
