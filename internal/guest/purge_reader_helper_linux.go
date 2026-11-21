//go:build linux

package guest

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"runtime"
	"unsafe"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

// This is a child of the actual reader PID1, over a socketpair it created.
// The private message selects only a fixed probe. It supplies no boot issuer,
// credentials, host path, caller executable or generic command authority.
func RunPurgeReaderGitHelperV1(args []string) error {
	if len(args) != 0 || os.Getpid() == 1 || os.Getppid() != 1 || os.Geteuid() != 0 || os.Getuid() != 0 {
		return purgeWorkErrorV1("the fixed reader command has no original root PID1 parent")
	}
	connection, err := purgeReaderChildControlV1()
	if err != nil {
		return err
	}
	var request proto.Request
	frame, readErr := proto.ReadLimit(bufio.NewReader(connection), 64<<10)
	if readErr != nil || frame.Type != proto.TRequest || !api.ExactJSONFields(frame.Payload, request) || json.Unmarshal(frame.Payload, &request) != nil || request.ID != 1 || request.Op != purgeReaderGitOperationV1 {
		return errors.Join(purgeWorkErrorV1("the fixed reader command has no original bounded request"), readErr, connection.Close())
	}
	var probe purgeReaderGitRequestV1
	if !api.ExactJSONFields(request.Args, probe) || json.Unmarshal(request.Args, &probe) != nil {
		return errors.Join(purgeWorkErrorV1("the fixed reader command request changed"), connection.Close())
	}
	if err := validatePurgeReaderGitV1(probe); err != nil {
		return errors.Join(err, connection.Close())
	}
	if err := connection.Close(); err != nil {
		return err
	}
	// Prctl/capability state is per native thread. Keep this same thread
	// through credential/security transition and the one fixed exec.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread() // only a returned error reaches this defer; successful exec replaces it.
	// The same fixed credential transition used by private file publishers
	// clears groups, capabilities and dumpability before any workspace access.
	if err := dropFileCommitAccount(); err != nil {
		return err
	}
	if err := os.Chdir(probe.Directory); err != nil {
		return err
	}
	command := bundleCommand("", probe.Directory, probe.Arguments...)
	// No optional parallel Git workers are available in this one-process
	// purpose. Any feature requiring a child is an unknown work observation.
	command.Argv = append(command.Argv[:1], append([]string{"-c", "index.threads=1", "-c", "core.preloadIndex=false"}, command.Argv[1:]...)...)
	if err := installPurgeReaderFilterV1(); err != nil {
		return err
	}
	return unix.Exec("/usr/bin/git", command.Argv, bundleEnvironment())
}

func purgeReaderChildControlV1() (net.Conn, error) {
	file := os.NewFile(3, "original-reader-command")
	if file == nil {
		return nil, purgeWorkErrorV1("the fixed reader command has no original control file")
	}
	raw, err := file.SyscallConn()
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	var peer *unix.Ucred
	var peerErr error
	check := raw.Control(func(fd uintptr) { peer, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) })
	if check != nil || peerErr != nil || peer == nil || peer.Pid != 1 || peer.Uid != 0 || peer.Gid != 0 {
		return nil, errors.Join(purgeWorkErrorV1("the private reader command peer is not its original native PID1"), check, peerErr, file.Close())
	}
	connection, err := net.FileConn(file)
	closed := file.Close()
	if err != nil || closed != nil {
		if connection != nil {
			closed = errors.Join(closed, connection.Close())
		}
		return nil, errors.Join(err, closed)
	}
	return connection, nil
}

func installPurgeReaderFilterV1() error {
	program, err := purgeReaderFilterV1(runtime.GOARCH)
	if err != nil {
		return err
	}
	filter := make([]unix.SockFilter, len(program))
	for index, instruction := range program {
		filter[index] = unix.SockFilter{Code: instruction.Op, Jt: instruction.Jt, Jf: instruction.Jf, K: instruction.K}
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	if len(filter) == 0 || len(filter) > 4096 {
		return purgeWorkErrorV1("the fixed reader syscall profile is unavailable")
	}
	programHeader := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]} //nolint:gosec // the length is bounded above.
	err = unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&programHeader)), 0, 0)
	runtime.KeepAlive(filter)
	return err
}
