//go:build linux

package slotlaunch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

// NativeSourceV1 can only be issued by an actual fixed-sudo transaction and
// kernel descriptor transfer from the currently executing root launcher.
// Neither its zero value nor response JSON can issue native VM authority.
type NativeSourceV1 struct {
	mu              sync.Mutex
	request         Request
	owner           context.Context
	files           []*os.File
	parent          *os.File
	identity        cgroupIdentity
	boot            string
	closed          bool
	closeErr        error
	closure         *nativeAllocationClosureV1
	adopted         bool
	attempt         *nativeStartAttemptV1
	reserved        *originalAllocationLeaseV2
	pendingLease    *os.File
	reservedDestroy []*os.File
	guest           net.Conn
	guestPeer       *os.File
	exitKnown       bool
	exitCode        int
	exitSignal      string
	exitErr         error
}

// StartNativeV1 performs a real native start and retains actual kernel-owned
// descriptors. Held inputs establish byte correlation only. The engine still
// needs its genuine original operation and complete protected VM Need.
func StartNativeV1(ctx context.Context, request Request, held []*os.File) (created *NativeSourceV1, returned error) {
	return startNativeWithInputsV2(ctx, request, held, nil)
}

type clientStartInputsV2 struct {
	fork     *os.File
	reserved []*os.File
}

// StartForkNativeV1 consumes only a staged raw DATA descriptor inside the
// genuine original new Boot callback. Root creates its own fresh allocation.
func StartForkNativeV1(ctx context.Context, request Request, held []*os.File, input *os.File) (*NativeSourceV1, error) {
	if input == nil {
		return nil, refused("the actual staged fork data descriptor is absent")
	}
	return startNativeWithInputsV2(ctx, request, held, &clientStartInputsV2{fork: input})
}

// StartReservedNativeV2 consumes a genuine original-new-Boot reservation loan.
// It does not adopt a saved record or grant host Source authority from DATA.
func StartReservedNativeV2(ctx context.Context, request Request, held []*os.File, original []*os.File) (*NativeSourceV1, error) {
	if len(original) != 18 {
		return nil, refused("the original new Boot reserved operands are incomplete")
	}
	if err := CheckOriginalReservationLeaseV2(original[17]); err != nil {
		return nil, err
	}
	return startNativeWithInputsV2(ctx, request, held, &clientStartInputsV2{reserved: original})
}

func startNativeWithInputsV2(ctx context.Context, request Request, held []*os.File, input *clientStartInputsV2) (created *NativeSourceV1, returned error) {
	if ctx == nil || ctx.Err() != nil || request.Op != "start" || request.validate() != nil || len(held) != 5 {
		return nil, refused("the native slot start has no complete original source")
	}
	parentGroup, err := captureCurrentCgroup()
	if err != nil {
		return nil, err
	}
	defer func() {
		if created == nil {
			parentGroup.Close()
		}
	}()
	for i, file := range held {
		if file == nil {
			return nil, refused("the original runtime role is missing")
		}
		st, err := file.Stat()
		if err != nil || !st.Mode().IsRegular() {
			return nil, refused("an original runtime role is not regular")
		}
		if err := StoreNativeV1(ctx, file, request.digests()[i], st.Size()); err != nil {
			return nil, err
		}
	}
	launcher, err := fixedLauncher()
	if err != nil {
		return nil, err
	}
	defer launcher.Close()
	launchStat, err := launcher.Stat()
	if err != nil {
		return nil, err
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parent := os.NewFile(uintptr(pair[0]), "native-slot-source")
	child := os.NewFile(uintptr(pair[1]), "native-root-launch")
	defer parent.Close()
	defer child.Close()
	if err := unix.SetsockoptInt(pair[0], unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		return nil, err
	}
	connection, err := net.FileConn(parent)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	socket, ok := connection.(*net.UnixConn)
	if !ok {
		return nil, refused("the native source is not a Unix socket")
	}
	command := exec.CommandContext(ctx, "/usr/bin/sudo", "-n", LauncherPath())
	command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
	command.Stdin, command.Stdout, command.Stderr = child, child, io.Discard
	source := &NativeSourceV1{request: request, owner: ctx, parent: parentGroup}
	if input != nil && len(input.reserved) > 0 {
		source.pendingLease, err = dupFile(input.reserved[17])
		if err != nil {
			return nil, err
		}
	}
	source.attempt = newNativeStartAttemptV1(command)
	defer func() {
		if returned == nil {
			return
		}
		if !source.adopted {
			// This actual launch attempt may already own a root allocator or
			// partial native handoff. Keep every original role and parent;
			// neither an error nor a monitor exit proves allocation absence.
			returned = errors.Join(returned, source.attempt.stopActors())
			return
		}
		returned = errors.Join(returned, source.Stop(context.WithoutCancel(ctx)))
		if source.PhysicalExitV1() {
			returned = errors.Join(returned, source.Close())
		}
	}()
	if err := source.attempt.start(); err != nil {
		return source, err
	}
	child.Close()
	if err := socket.SetDeadline(time.Now().Add(45 * time.Second)); err != nil {
		return source, err
	}
	wire := request
	if input != nil {
		if input.fork != nil {
			wire.Op = "start.fork"
		} else {
			wire.Op = "start.reserved-v2"
		}
	}
	if err := json.NewEncoder(socket).Encode(wire); err != nil {
		return source, err
	}
	if input != nil {
		if err := sendOriginalStartInputsV2(socket, source.attempt, launchStat, input, parentGroup); err != nil {
			return source, err
		}
	}
	response, files, credential, err := receiveNative(socket)
	source.files = files
	if err != nil {
		return source, err
	}
	if !response.OK && len(files) == 0 {
		if credential == nil || credential.Uid != 0 {
			return nil, refused("the native start refusal has no original root sender")
		}
		if response.Code == "wall_unavailable" {
			return nil, &proto.Error{Code: response.Code, Message: response.Error, Hint: response.Hint}
		}
		if response.Code == "no_room" || response.Code == "launcher_refused" {
			return source, &launchError{Code: response.Code, Message: response.Error, Hint: response.Hint}
		}
		return source, refused("the root launcher refused the native start")
	}
	if !response.OK || len(files) != 13 || credential == nil || credential.Uid != 0 {
		return source, refused("the native reply has no complete kernel root credentials and descriptor vector")
	}
	pid, err := nativePID(files[12])
	if err != nil || pid != int(credential.Pid) || !pidLive(files[12]) {
		return source, refused("the native sender creation handle differs from kernel SCM credentials")
	}
	actualExe, err := files[11].Stat()
	if err != nil || !singleRegular(actualExe, 0) || !os.SameFile(launchStat, actualExe) {
		return source, refused("the actual root sender executed another launcher object")
	}
	if err := source.attempt.captureRootWorker(files[12]); err != nil {
		return source, err
	}
	source.adopted = true
	boot, err := nativeBootID()
	if err != nil {
		return source, err
	}
	source.boot = boot
	cgStat, err := files[2].Stat()
	if err != nil || !owned(cgStat, 0) || !cgStat.IsDir() {
		return source, refused("the native source has no root-created cgroup")
	}
	native := cgStat.Sys().(*syscall.Stat_t)
	path, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(int(files[2].Fd())))
	if err != nil || !strings.HasPrefix(path, "/sys/fs/cgroup/") {
		return source, refused("the original cgroup descriptor has no native mount identity")
	}
	parentStat, err := cgroupParentStat(source.files[2])
	if err != nil {
		return source, err
	}
	source.identity = cgroupIdentity{Path: strings.TrimPrefix(path, "/sys/fs/cgroup"), Dev: uint64(native.Dev), Ino: native.Ino, ParentDev: uint64(parentStat.Dev), ParentIno: parentStat.Ino}
	if err := source.check(ctx, request); err != nil {
		return source, err
	}
	if _, err := socket.Write([]byte{1}); err != nil {
		return source, err
	}
	err = source.attempt.waitCreator()
	if err != nil {
		return source, refused("the original root source failed after its native handoff")
	}
	return source, nil
}

func (s *NativeSourceV1) check(ctx context.Context, request Request) error {
	if s == nil || !s.adopted || s.closed || s.owner == nil || s.owner.Err() != nil || ctx == nil || ctx.Err() != nil || len(s.files) != 13 || !reflect.DeepEqual(s.request, request) || !pidLive(s.files[0]) {
		return refused("the original native slot source ended or changed")
	}
	boot, err := nativeBootID()
	if err != nil || boot != s.boot {
		return refused("the native slot source belongs to another boot")
	}
	if !pidLive(s.files[1]) {
		return refused("the original slot namespace init ended")
	}
	if err := sameNativeCgroupParent(s.files[2], s.parent); err != nil {
		return err
	}
	currentParent, err := captureCurrentCgroup()
	if err != nil {
		return err
	}
	left, err := currentParent.Stat()
	currentParent.Close()
	if err != nil {
		return err
	}
	right, err := s.parent.Stat()
	if err != nil || !os.SameFile(left, right) {
		return refused("the native source caller left its original session cgroup")
	}
	if err := checkSlotVMM(s.files[3], request.VMM); err != nil {
		return err
	}
	root, err := cgroupRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	same, err := openCgroup(root, s.identity)
	if err != nil {
		return err
	}
	defer same.Close()
	before, err := same.Stat()
	if err != nil {
		return err
	}
	held, err := s.files[2].Stat()
	if err != nil || !os.SameFile(before, held) {
		return refused("the native slot cgroup object changed")
	}
	live, err := populated(s.files[2])
	if err != nil || !live {
		return refused("the original native slot cgroup is no longer populated")
	}
	for i, digest := range request.digests() {
		b := &backend{owner: 0}
		if err := b.checkStore(s.files[4+i], digest, 0); err != nil {
			return err
		}
		flags, err := unix.FcntlInt(s.files[4+i].Fd(), unix.F_GETFL, 0)
		if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
			return refused("a native immutable role is not read-only")
		}
	}
	disk, err := s.files[9].Stat()
	if err != nil || !disk.Mode().IsRegular() || disk.Sys().(*syscall.Stat_t).Nlink != 1 || disk.Size() != request.DiskMaxGiB*(1<<30) {
		return refused("the native mutable disk role changed")
	}
	return nil
}

// SameSourceV1 validates this exact original source against the engine's
// complete original boot projection. It never accepts a replacement source.
func (s *NativeSourceV1) SameSourceV1(ctx context.Context, request Request) error {
	if s == nil || !s.adopted {
		return refused("the native source is absent")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.check(ctx, request)
}
func (s *NativeSourceV1) PhysicalExitV1() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closure != nil {
		return s.closure.check()
	}
	if !s.adopted || len(s.files) != 13 {
		return false
	}
	if !pidExited(s.files[0]) || !pidExited(s.files[1]) || s.guestPeer != nil && !pidExited(s.guestPeer) {
		return false
	}
	var native unix.Stat_t
	var filesystem unix.Statfs_t
	if s.parent == nil || s.identity.Dev == 0 || s.identity.Ino == 0 || unix.Fstat(int(s.files[2].Fd()), &native) != nil || unix.Fstatfs(int(s.files[2].Fd()), &filesystem) != nil || filesystem.Type != unix.CGROUP2_SUPER_MAGIC || native.Mode&unix.S_IFMT != unix.S_IFDIR || uint64(native.Dev) != s.identity.Dev || native.Ino != s.identity.Ino {
		return false
	}
	// The kernel cannot unlink a populated cgroup. This is positive native
	// vacancy of the SAME originally root-issued inode, never path absence.
	if native.Nlink == 0 {
		return true
	}
	live, err := populated(s.files[2])
	return err == nil && !live
}
func (s *NativeSourceV1) Wait() error {
	if s == nil || s.owner == nil {
		return refused("the native source is absent")
	}
	if !s.adopted {
		if s.attempt == nil {
			return refused("the native source has no actual start-attempt owner")
		}
		return errors.Join(s.attempt.waitCreator(), refused("the actual root start attempt has no proven whole-allocation closure; retain its original unit and operation"))
	}
	for !s.PhysicalExitV1() {
		recover, err := s.orphanRecoveryNeeded()
		if err != nil {
			return err
		}
		if recover {
			if err := s.Stop(context.Background()); err != nil {
				return err
			}
			continue
		}
		select {
		case <-s.owner.Done():
			_ = s.Stop(context.Background())
			time.Sleep(20 * time.Millisecond)
		case <-time.After(20 * time.Millisecond):
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.exitKnown {
		connection, err := net.FileConn(s.files[10])
		if err != nil {
			return err
		}
		defer connection.Close()
		_ = connection.SetReadDeadline(time.Now().Add(time.Second))
		var response Response
		dec := json.NewDecoder(io.LimitReader(connection, RequestMax))
		dec.DisallowUnknownFields()
		if dec.Decode(&response) != nil || !response.OK || response.ExitCode == nil {
			return refused("the actual keeper ended without its owned VMM exit status")
		}
		s.exitKnown, s.exitCode, s.exitSignal = true, *response.ExitCode, response.ExitSignal
		if response.Error != "" {
			s.exitErr = refused("the original slot VMM wait failed: " + response.Error)
		}
	}
	if s.exitErr != nil {
		return s.exitErr
	}
	if s.exitCode != 0 || s.exitSignal != "" {
		return refused("the slot VMM ended with a non-zero status or signal")
	}
	return nil
}

// ExitStatusV1 reports only the actual keeper's joined native wait result.
func (s *NativeSourceV1) ExitStatusV1() (int, string, bool) {
	if s == nil {
		return -1, "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitCode, s.exitSignal, s.exitKnown
}
