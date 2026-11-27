//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/godbus/dbus/v5"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"golang.org/x/sys/unix"
)

// custodyInheritanceV1 originates only in this service main's actual inherited
// kernel bundle. Role names and numbers select handles, never grant authority.
type custodyInheritanceV1 struct {
	mu                  sync.Mutex
	native              *sessionFileLease
	manager             *nativeUserManagerV1
	path                dbus.ObjectPath
	activation          unitActivationProperties
	root, cgroup, pidfd *os.File
	bootstrapPIDFD      *os.File
	claim               *custodyClaimV1
	consumed            atomic.Bool
	released            atomic.Bool
	closeErr            error
	managerDiagnostic   error
	retirement          nativeStartupRetirementsV1
	unclassified        []*os.File
	preparedRoot        *os.Root
	closed              atomic.Bool
}

func inheritManagerCustodyV1(ctx context.Context, id string) (partial *custodyInheritanceV1, present bool, failure error) {
	pid, fds, names := os.Getenv("LISTEN_PID"), os.Getenv("LISTEN_FDS"), os.Getenv("LISTEN_FDNAMES")
	if pid == "" && fds == "" && names == "" {
		return nil, false, nil
	}
	for _, name := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		_ = os.Unsetenv(name)
	}
	if pid != strconv.Itoa(os.Getpid()) || fds != strconv.Itoa(custodyBundleSizeV1) {
		return nil, true, sessionLeaseError("the service has no exact current-main inherited custody bundle")
	}
	roles := strings.Split(names, ":")
	if len(roles) != custodyBundleSizeV1 {
		return nil, true, sessionLeaseError("the inherited custody bundle has another native role count")
	}
	inherited := &custodyInheritanceV1{}
	files := make(map[string]*os.File, custodyBundleSizeV1)
	complete := false
	defer func() {
		if !complete {
			cleanup := inherited.Close()
			failure = errors.Join(failure, cleanup, inherited.managerDiagnostic, inherited.retirement.diagnosticsV4(), inherited.claim.closeDiagnosticsV1())
			if cleanup != nil {
				partial = inherited
			}
		}
	}()
	for index, role := range roles {
		fd := index + 3
		unix.CloseOnExec(fd)
		file := os.NewFile(uintptr(fd), "inherited-"+role)
		if file == nil {
			return nil, true, sessionLeaseError("an inherited custody kernel handle is absent")
		}
		inherited.unclassified = append(inherited.unclassified, file)
		switch role {
		case "custody-root-v1", "custody-holder-v1", "custody-cgroup-v1", "custody-pidfd-v1", "custody-claim-v1":
		default:
			return nil, true, errors.Join(sessionLeaseError("the inherited custody bundle has an unknown native role"))
		}
		if files[role] != nil {
			return nil, true, errors.Join(sessionLeaseError("the inherited custody bundle repeats a native role"))
		}
		files[role] = file
	}
	root, err := cachefs.OpenPrivateRoot(paths.SessionDir(id), false)
	if err != nil {
		return nil, true, err
	}
	inherited.preparedRoot = root
	rootRef, err := cachefs.CaptureRoot(root)
	if err != nil {
		return nil, true, err
	}
	held, err := files["custody-root-v1"].Stat()
	named, statErr := root.Stat(".")
	if err != nil || statErr != nil || !held.IsDir() || !os.SameFile(held, named) {
		return nil, true, errors.Join(sessionLeaseError("the inherited custody root lost its exact named session identity"), err, statErr)
	}
	holder := files["custody-holder-v1"]
	fileRef, err := cachefs.CaptureFile(root, holderLockFile, holder, 0o600)
	if err != nil {
		return nil, true, err
	}
	if err := checkCustodyFlockV1(holder); err != nil {
		return nil, true, err
	}
	inherited.native = &sessionFileLease{root: root, file: holder, rootRef: rootRef, fileRef: fileRef, id: id, path: paths.SessionDir(id), name: holderLockFile, borrowed: true}
	delete(files, "custody-holder-v1")
	inherited.root, inherited.cgroup, inherited.pidfd = files["custody-root-v1"], files["custody-cgroup-v1"], files["custody-pidfd-v1"]
	inherited.claim = &custodyClaimV1{file: files["custody-claim-v1"]}
	inherited.unclassified = nil
	for role := range files {
		delete(files, role)
	}
	inherited.manager, err = openNativeUserManagerV1(ctx)
	if err != nil {
		return nil, true, err
	}
	if err := inherited.manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".GetUnitByPID", 0, uint32(os.Getpid())).Store(&inherited.path); err != nil {
		return nil, true, err
	}
	inherited.activation, err = inherited.manager.activation(ctx, inherited.path, UnitName(id))
	if err != nil || inherited.activation.MainPID != uint32(os.Getpid()) {
		return nil, true, errors.Join(sessionLeaseError("custody inheritance is outside its same canonical service main"), err)
	}
	selfPIDFD, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		return nil, true, err
	}
	inherited.bootstrapPIDFD = os.NewFile(uintptr(selfPIDFD), "original-current-custody-bootstrap-pidfd")
	if enabled, err := inherited.manager.custodyPropertiesV1(ctx, inherited.path); err != nil || !enabled {
		return nil, true, errors.Join(sessionLeaseError("the inherited service has no closed manager custody properties"), err)
	}
	if err := inherited.check(ctx); err != nil {
		return nil, true, err
	}
	complete = true
	return inherited, true, nil
}

func (inherited *custodyInheritanceV1) check(ctx context.Context) error {
	if inherited == nil || inherited.closed.Load() || inherited.native == nil || inherited.consumed.Load() {
		return sessionLeaseError("the inherited closed manager custody ended or was consumed")
	}
	if err := errors.Join(inherited.native.check(ctx), checkCustodyFlockV1(inherited.native.file), inherited.manager.checkNativeManager(), inherited.claim.peek()); err != nil {
		return err
	}
	if err := inherited.checkPassiveOldClosureV1(ctx); err != nil {
		return err
	}
	current, err := inherited.manager.activation(ctx, inherited.path, UnitName(inherited.native.id))
	if err != nil || !sameUnitActivationProperties(current, inherited.activation) || current.MainPID != uint32(os.Getpid()) {
		return errors.Join(sessionLeaseError("the inherited bootstrap's own native invocation changed"), err)
	}
	if err := inherited.manager.checkCustodyPinV1(ctx, UnitName(inherited.native.id)); err != nil {
		return err
	}
	return inherited.verifyStore(ctx)
}

func checkCustodyExitedMainV1(pidfd *os.File) error {
	if pidfd == nil {
		return sessionLeaseError("the original custody exit handles are absent")
	}
	var fs unix.Statfs_t
	if err := unix.Fstatfs(int(pidfd.Fd()), &fs); err != nil || fs.Type != unix.ANON_INODE_FS_MAGIC && fs.Type != unix.PID_FS_MAGIC {
		return errors.Join(sessionLeaseError("the original custody exit handle is not native pidfd storage"), err)
	}
	polls := []unix.PollFd{{Fd: int32(pidfd.Fd()), Events: unix.POLLIN}}
	n, err := unix.Poll(polls, 0)
	if err != nil || n != 1 || polls[0].Revents&unix.POLLIN == 0 || polls[0].Revents&unix.POLLNVAL != 0 {
		return errors.Join(sessionLeaseError("the original custody main has no positive native exit"), err)
	}
	// A readable eventfd cannot impersonate a pidfd: the kernel signal query
	// must recognise the actual exited process object and report ESRCH.
	if err := unix.PidfdSendSignal(int(pidfd.Fd()), 0, nil, 0); !errors.Is(err, unix.ESRCH) {
		return errors.Join(sessionLeaseError("the original custody role is not an exited native pidfd"), err)
	}
	return nil
}

func checkCustodyOldClosureV1(pidfd, cgroup *os.File) error {
	if err := checkCustodyExitedMainV1(pidfd); err != nil {
		return err
	}
	empty, err := originalStopCgroupEmptyV1(cgroup)
	if err != nil || !empty {
		return errors.Join(sessionLeaseError("the original custody cgroup still has native writers"), err)
	}
	return nil
}

func (inherited *custodyInheritanceV1) Close() error {
	if inherited == nil {
		return nil
	}
	inherited.mu.Lock()
	defer inherited.mu.Unlock()
	if inherited.closed.Load() {
		return inherited.closeErr
	}
	files := append([]*os.File{inherited.root, inherited.cgroup, inherited.pidfd, inherited.bootstrapPIDFD}, inherited.unclassified...)
	_ = inherited.retirement.closeFilesV4(files)
	_ = inherited.retirement.closeRootsV4([]*os.Root{inherited.preparedRoot})
	var native error
	if inherited.native != nil {
		_ = inherited.retirement.closeSessionLeaseV4(inherited.native)
		native = inherited.retirement.checkSessionLeaseV4(inherited.native)
	}
	primary := ""
	if inherited.native != nil {
		primary = inherited.native.id
	}
	manager := closeNativeManagerResourceV4(inherited.manager, primary, &inherited.managerDiagnostic)
	inherited.closeErr = errors.Join(inherited.retirement.checkFilesV4(files), inherited.retirement.checkRootsV4([]*os.Root{inherited.preparedRoot}), native, inherited.claim.Close(), manager)
	if inherited.closeErr == nil {
		inherited.closed.Store(true)
	}
	return inherited.closeErr
}
