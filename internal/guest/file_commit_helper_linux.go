//go:build linux

package guest

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/filecopy"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/secretmem"
	"golang.org/x/sys/unix"
)

// RunVMFileCommitHelper starts only as a protected child of actual VM PID1.
// It drops every credential/group/capability before opening any destination.
// Fixed inherited roots came from the private original-mount producer; the
// caller supplies no path to open as root, descriptor index or permission bit.
func RunVMFileCommitHelper(args []string) (returned error) {
	if len(args) != 0 || os.Geteuid() != 0 || os.Getuid() != 0 {
		return putError("the private VM publisher requires its original root producer and no arguments")
	}
	if err := secretmem.Protect(); err != nil {
		return err
	}
	connection, err := adoptFileCommitControlV1()
	if err != nil {
		return err
	}
	defer connection.Close()
	line, err := host.NewStartupLineV1(connection)
	if err != nil {
		return err
	}
	defer line.Close()
	frame, err := line.ReadFrame()
	if err != nil {
		return err
	}
	if err := line.RejectAncillary(); err != nil {
		return err
	}
	var request proto.Request
	if frame.Type != proto.TRequest || !api.ExactJSONFields(frame.Payload, request) || json.Unmarshal(frame.Payload, &request) != nil || request.ID != 1 || request.Op != proto.VMFileCommitOperationV1 {
		return putError("invalid private VM publication request")
	}
	var commit proto.VMFileCommitV1
	if !api.ExactJSONFields(request.Args, commit) || json.Unmarshal(request.Args, &commit) != nil || commit.Version != 1 || !path.IsAbs(commit.Mount) || path.Clean(commit.Mount) != commit.Mount {
		return putError("invalid original copy mount projection")
	}
	top, relative, err := putTarget(commit.Header)
	if err != nil || commit.Header.Path == "" || commit.Header.Directory {
		return putError("private publication requires one explicit file")
	}
	guestPath := path.Join(top, relative)
	if !strings.HasPrefix(guestPath, commit.Mount+"/") {
		return putError("the file leaves its actual original copy mount")
	}
	relative = strings.TrimPrefix(guestPath, commit.Mount+"/")
	if err := filecopy.Relative(relative); err != nil {
		return err
	}
	stage, err := fileCommitRootV1(4)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, stage.Close()) }()
	original, err := fileCommitRootV1(5)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, original.Close()) }()
	visible, err := fileCommitRootV1(6)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, visible.Close()) }()
	if err := dropFileCommitAccount(); err != nil {
		return err
	}
	// Both walks happen under the actual fixed account. The visible walk
	// preserves search permissions above a nested retained mount; identity
	// comparison is necessary continuity evidence, never the mount grant.
	visibleParent, err := filecopy.OpenDirectoryRoot(visible, strings.TrimPrefix(path.Dir(guestPath), "/"), true)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, visibleParent.Close()) }()
	destination, err := filecopy.OpenDirectoryRoot(original, path.Dir(relative), false)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, destination.Close()) }()
	visibleInfo, err := visibleParent.Stat(".")
	if err != nil {
		return err
	}
	originalInfo, err := destination.Stat(".")
	if err != nil || !os.SameFile(visibleInfo, originalInfo) {
		return putError("the actual guest destination changed from its original mount resource")
	}
	if err := renameFileCommitV1(stage, destination, path.Base(relative), commit.Header.Replace); err != nil {
		return err
	}
	return line.WriteFrame(proto.TResponse, proto.Response{ID: 1, OK: true})
}

func adoptFileCommitControlV1() (*net.UnixConn, error) {
	file := os.NewFile(3, "private-copy-control")
	if file == nil {
		return nil, putError("the private copy control descriptor is absent")
	}
	unix.CloseOnExec(3)
	connection, err := net.FileConn(file)
	file.Close()
	if err != nil {
		return nil, err
	}
	control, ok := connection.(*net.UnixConn)
	if !ok {
		return nil, errors.Join(putError("the copy role has no original private Unix control channel"), connection.Close())
	}
	raw, err := control.SyscallConn()
	if err != nil {
		return nil, errors.Join(err, control.Close())
	}
	var peer *unix.Ucred
	var checkErr error
	err = raw.Control(func(fd uintptr) {
		if uint64(fd) > uint64(^uint(0)>>1) {
			checkErr = putError("the private control descriptor is invalid")
			return
		}
		peer, checkErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if err != nil || checkErr != nil || peer == nil || peer.Pid != 1 || peer.Uid != 0 || peer.Gid != 0 {
		return nil, errors.Join(putError("the private copy role has no actual VM PID1 parent"), err, checkErr, control.Close())
	}
	return control, nil
}

func fileCommitFDPath(fd int) string { return "/proc/self/fd/" + strconv.Itoa(fd) }

func fileCommitUntraced() error {
	file, err := os.Open("/proc/self/status")
	if err != nil {
		return err
	}
	defer file.Close()
	raw, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var filesystem unix.Statfs_t
	var statErr error
	err = raw.Control(func(fd uintptr) {
		if uint64(fd) > uint64(^uint(0)>>1) {
			statErr = putError("the private status descriptor is invalid")
			return
		}
		statErr = unix.Fstatfs(int(fd), &filesystem)
	})
	if err != nil || statErr != nil || filesystem.Type != unix.PROC_SUPER_MAGIC {
		return putError("the private publisher has no genuine procfs state")
	}
	data, err := io.ReadAll(io.LimitReader(file, 64<<10))
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "TracerPid:") {
			if strings.TrimSpace(strings.TrimPrefix(line, "TracerPid:")) != "0" {
				return putError("the private publisher was traced before account protection completed")
			}
			return nil
		}
	}
	return putError("the private publisher cannot establish its tracing state")
}

// fileCommitRootV1 duplicates a fixed inherited role while it is still held,
// then compares native identity. /proc is a descriptor locator here; it never
// turns a caller pathname, number or claimed metadata into a resource owner.
func fileCommitRootV1(fd int) (*os.Root, error) {
	file := os.NewFile(uintptr(fd), "private-copy-root")
	if file == nil {
		return nil, putError("the private copy root descriptor is absent")
	}
	defer file.Close()
	unix.CloseOnExec(fd)
	want, err := file.Stat()
	if err != nil || !want.IsDir() {
		return nil, putError("the private copy role is not a directory")
	}
	root, err := os.OpenRoot(fileCommitFDPath(fd))
	if err != nil {
		return nil, err
	}
	got, err := root.Stat(".")
	if err != nil || !os.SameFile(want, got) {
		return nil, errors.Join(putError("the private copy root changed while it was being adopted"), err, root.Close())
	}
	return root, nil
}
