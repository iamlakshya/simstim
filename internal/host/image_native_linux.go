//go:build linux

package host

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/image"
	"golang.org/x/sys/unix"
)

// imageNativeGuardianV1 is issued around the actual private exec.Cmd before
// creation. The ready DTO can correlate a scope, but cannot construct this
// owner or authorise cleanup. All handles below come from its real creation
// and the actual live creator's native cgroup membership before plan release.
type imageNativeGuardianV1 struct {
	mu                                       sync.Mutex
	manager                                  *nativeUserManagerV1
	unitPath                                 dbus.ObjectPath
	watchStop, watchDone                     chan struct{}
	vacant                                   bool
	watchErr                                 error
	ctx                                      context.Context
	command                                  *exec.Cmd
	helper                                   *os.File // borrowed from the original converter owner
	unit                                     string
	created                                  int
	process, task                            *os.File
	group, parent, events, kill              *os.File
	birth                                    uint64
	groupRef, parentRef                      os.FileInfo
	started, noCreation, planReleased, bound bool
	managerDiagnostic                        error
}

func newImageNativeGuardianV1(ctx context.Context, command *exec.Cmd, helper *os.File, unit string) (*imageNativeGuardianV1, error) {
	if ctx == nil || command == nil || helper == nil {
		return nil, imageNativeFailureV1("the image cleanup source is not its original closed guardian")
	}
	if err := validateConverterUnit(unit); err != nil {
		return nil, err
	}
	count := len(command.ExtraFiles) - 2
	if count < 0 || count > image.MaxLayers || command.Path != "/proc/self/fd/"+strconv.Itoa(3+count) || len(command.Args) == 0 || command.Args[0] != command.Path || command.ExtraFiles[count] != helper || command.Process != nil {
		return nil, imageNativeFailureV1("the image cleanup source lost its original helper/output vector")
	}
	closed := len(command.Args) == 4 && command.Args[1] == "image-guardian" && command.Args[3] == unit && (command.Args[2] == strconv.Itoa(count) || count == 0 && command.Args[2] == savedRootConverterMode)
	closed = closed || count == 0 && len(command.Args) == 3 && command.Args[1] == "agents-guardian" && command.Args[2] == unit
	if !closed {
		return nil, imageNativeFailureV1("the image cleanup source has another closed guardian operation")
	}
	owner := &imageNativeGuardianV1{ctx: ctx, command: command, helper: helper, unit: unit, created: -1}
	attr := syscall.SysProcAttr{}
	if command.SysProcAttr != nil {
		attr = *command.SysProcAttr
	}
	if attr.PidFD != nil {
		return nil, imageNativeFailureV1("the guardian creation already belongs to another native owner")
	}
	attr.PidFD = &owner.created
	command.SysProcAttr = &attr
	return owner, nil
}
func (owner *imageNativeGuardianV1) childStartedV1(startErr error) error {
	if owner == nil || owner.started {
		return imageNativeFailureV1("the original guardian creation was already consumed")
	}
	owner.started = true
	if owner.created >= 0 {
		owner.process = os.NewFile(uintptr(owner.created), "original-image-guardian-creation")
	}
	if startErr != nil {
		if owner.command.Process == nil && owner.created < 0 {
			owner.noCreation = true
			return nil
		}
		return imageNativeFailureV1("the original guardian creation result is ambiguous")
	}
	if owner.created < 0 || owner.command.Process == nil {
		return imageNativeFailureV1("the original guardian has no actual atomic creation pidfd")
	}
	if err := owner.checkLiveV1(); err != nil {
		return err
	}
	pid, err := imageGuardianPIDV1(owner.process)
	if err != nil {
		return err
	}
	if pid != owner.command.Process.Pid {
		return imageNativeFailureV1("the actual guardian differs from its atomic creation descriptor")
	}
	fd, err := unix.Open("/proc/"+strconv.Itoa(pid), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	owner.task = os.NewFile(uintptr(fd), "original-image-guardian-task")
	birth, parent, err := imageGuardianTaskV1(owner.task, pid)
	if err != nil {
		return err
	}
	if parent != os.Getpid() {
		return imageNativeFailureV1("the original guardian has another actual creator")
	}
	owner.birth = birth
	return owner.checkCodeV1()
}
func (owner *imageNativeGuardianV1) checkLiveV1() error {
	if owner == nil || owner.process == nil {
		return imageNativeFailureV1("the original guardian creation source is missing")
	}
	ready, err := imageGuardianExitedV1(owner.process)
	if err != nil || ready {
		return errors.Join(err, imageNativeFailureV1("the original guardian ended before its native scope binding"))
	}
	return nil
}
func (owner *imageNativeGuardianV1) checkCodeV1() error {
	if err := owner.checkLiveV1(); err != nil {
		return err
	}
	fd, err := unix.Openat(int(owner.task.Fd()), "exe", unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var actual, expected unix.Stat_t
	if err := errors.Join(unix.Fstat(fd, &actual), unix.Fstat(int(owner.helper.Fd()), &expected)); err != nil {
		return err
	}
	if actual.Mode&unix.S_IFMT != unix.S_IFREG || actual.Dev != expected.Dev || actual.Ino != expected.Ino || actual.Size != expected.Size {
		return imageNativeFailureV1("the original guardian runs another actual helper executable")
	}
	pid, err := imageGuardianPIDV1(owner.process)
	if err != nil {
		return err
	}
	birth, parent, err := imageGuardianTaskV1(owner.task, pid)
	if err != nil {
		return err
	}
	if birth != owner.birth || parent != os.Getpid() {
		return imageNativeFailureV1("the original guardian changed its native task identity")
	}
	return owner.checkLiveV1()
}
func (owner *imageNativeGuardianV1) bindScopeV1(ctx context.Context, scope imageScopeInfo) error {
	if owner == nil || owner.bound || owner.planReleased {
		return imageNativeFailureV1("the actual image scope was bound outside its original pre-plan phase")
	}
	if err := owner.checkCodeV1(); err != nil {
		return err
	}
	membership, err := imageGuardianMembershipV1(owner.task)
	if err != nil {
		return err
	}
	// The current fixed helper just created this unique unit with systemd's
	// exclusive "fail" operation. It cannot emit ready or consume a plan until
	// attachment succeeds. Current executable, native membership, exact private
	// leaf and the controlled parent all corroborate that real producer path.
	if membership != scope.ControlGroup || path.Base(membership) != owner.unit || path.Base(path.Dir(membership)) != brand.Slice || membership == "/" || !validImageInvocation(scope.InvocationID) {
		return imageNativeFailureV1("the actual guardian is not in its original private operation-created scope")
	}
	rootFD, err := unix.Open("/sys/fs/cgroup", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer unix.Close(rootFD)
	var fs unix.Statfs_t
	if err := unix.Fstatfs(rootFD, &fs); err != nil {
		return err
	}
	if fs.Type != unix.CGROUP2_SUPER_MAGIC {
		return imageNativeFailureV1("the original image scope is not on native cgroup v2")
	}
	open := func(name string) (*os.File, error) {
		fd, err := unix.Openat2(rootFD, name, &unix.OpenHow{Flags: uint64(unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC), Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS})
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(fd), "original-image-scope-directory"), nil
	}
	group, err := open(strings.TrimPrefix(membership, "/"))
	if err != nil {
		return err
	}
	parent, err := open(strings.TrimPrefix(path.Dir(membership), "/"))
	if err != nil {
		group.Close()
		return err
	}
	events, err := imageScopeLeafV1(group, "cgroup.events", unix.O_RDONLY)
	if err != nil {
		group.Close()
		parent.Close()
		return err
	}
	kill, err := imageScopeLeafV1(group, "cgroup.kill", unix.O_WRONLY)
	if err != nil {
		group.Close()
		parent.Close()
		events.Close()
		return err
	}
	groupInfo, err := group.Stat()
	parentInfo, parentErr := parent.Stat()
	if err != nil || parentErr != nil {
		group.Close()
		parent.Close()
		events.Close()
		kill.Close()
		return errors.Join(err, parentErr)
	}
	owner.group, owner.parent, owner.events, owner.kill = group, parent, events, kill
	owner.groupRef, owner.parentRef = groupInfo, parentInfo
	empty, err := readImageScopeVacancyV1(events)
	if err != nil {
		return err
	}
	if empty {
		return imageNativeFailureV1("the actual live guardian is absent from its captured native scope")
	}
	if err := owner.retainManagerScopeV1(ctx, scope); err != nil {
		return err
	}
	again, err := imageGuardianMembershipV1(owner.task)
	if err != nil {
		return err
	}
	if again != membership {
		return imageNativeFailureV1("the actual guardian changed its original native cgroup during binding")
	}
	if err := owner.checkCodeV1(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := owner.checkOriginalGroupV1(); err != nil {
		return err
	}
	owner.bound = true
	owner.watchStop, owner.watchDone = make(chan struct{}), make(chan struct{})
	go owner.watchVacancyV1()
	return nil
}
func (owner *imageNativeGuardianV1) releasePlanV1() error {
	if owner == nil || !owner.bound || owner.planReleased {
		return imageNativeFailureV1("the converter plan has no original native scope owner")
	}
	if err := owner.checkCodeV1(); err != nil {
		return err
	}
	if err := owner.checkOriginalGroupV1(); err != nil {
		return err
	}
	owner.planReleased = true // Set before the first possible continuation byte.
	return nil
}

// joinV1 intentionally retains this call stack, descriptors and publication
// reservation while the original native closure is unknown. Cancellation,
// scope names, manager replies and deadlines are never release permission.
func (owner *imageNativeGuardianV1) joinV1() error {
	if owner == nil {
		return nil
	}
	if owner.noCreation {
		return nil
	}
	var observed error
	observe := func(err error) {
		if err != nil && observed == nil {
			observed = err
		}
	}
	for {
		exited := false
		if owner.process != nil {
			ready, err := imageGuardianExitedV1(owner.process)
			if err != nil {
				observe(err)
			} else {
				exited = ready
			}
		}
		vacant := !owner.planReleased
		if owner.planReleased && owner.bound {
			owner.mu.Lock()
			vacant = owner.vacant
			watchErr := owner.watchErr
			owner.mu.Unlock()
			observe(watchErr)
		}
		if exited && vacant {
			return observed
		}
		if !exited && owner.process != nil {
			_ = unix.PidfdSendSignal(int(owner.process.Fd()), unix.SIGKILL, nil, 0)
		}
		if owner.planReleased && owner.bound && !vacant {
			if err := owner.checkOriginalGroupV1(); err != nil {
				observe(err)
			} else {
				n, err := owner.kill.WriteAt([]byte("1\n"), 0)
				if err == nil && n != 2 {
					err = io.ErrShortWrite
				}
				observe(err)
			}
		}
		// This wait only bounds polling. It cannot make an unknown source dead.
		time.Sleep(100 * time.Millisecond)
	}

}
func (owner *imageNativeGuardianV1) closeV1() error {
	if owner == nil {
		return nil
	}
	if owner.watchStop != nil {
		close(owner.watchStop)
		<-owner.watchDone
	}
	var result error
	if owner.manager != nil {
		if owner.unitPath != "" {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(owner.ctx), 5*time.Second)
			result = errors.Join(result, owner.manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(cleanup, systemdManagerInterface+".UnrefUnit", 0, owner.unit).Err)
			cancel()
		}
		result = errors.Join(result, closeNativeManagerResourceV4(owner.manager, "", &owner.managerDiagnostic))
	}
	for _, file := range []*os.File{owner.process, owner.task, owner.group, owner.parent, owner.events, owner.kill} {
		if file != nil {
			result = errors.Join(result, file.Close())
		}
	}
	return result
}
func imageScopeLeafV1(group *os.File, name string, flags int) (*os.File, error) {
	fd, err := unix.Openat(int(group.Fd()), name, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	var fs unix.Statfs_t
	if err := unix.Fstatfs(fd, &fs); err != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
		unix.Close(fd)
		return nil, errors.Join(err, imageNativeFailureV1("the original scope control escaped native cgroup v2"))
	}
	return os.NewFile(uintptr(fd), "original-image-"+name), nil
}
func readImageScopeVacancyV1(events *os.File) (bool, error) {
	if _, err := events.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	data, err := io.ReadAll(io.LimitReader(events, 4097))
	if err != nil {
		return false, err
	}
	if len(data) > 4096 {
		return false, imageNativeFailureV1("the original cgroup events exceed their native bound")
	}
	found := false
	empty := false
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return false, imageNativeFailureV1("the original cgroup events are malformed")
		}
		if fields[0] != "populated" {
			continue
		}
		if found || fields[1] != "0" && fields[1] != "1" {
			return false, imageNativeFailureV1("the original cgroup population is ambiguous")
		}
		found = true
		empty = fields[1] == "0"
	}
	if !found {
		return false, imageNativeFailureV1("the original cgroup has no native population evidence")
	}
	return empty, nil
}
func markImageGuardianPrivateV1(files []*os.File) error {
	for _, file := range files {
		if file == nil {
			return imageNativeFailureV1("the original guardian inherited a missing private capability")
		}
		flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
		if err != nil {
			return err
		}
		if _, err := unix.FcntlInt(file.Fd(), unix.F_SETFD, flags|unix.FD_CLOEXEC); err != nil {
			return err
		}
	}
	return nil
}
func imageNativeFailureV1(message string) error {
	return UserError("image_native", message, "Retain the original image operation and inspect the host with host doctor.")
}

func (owner *imageNativeGuardianV1) checkOriginalGroupV1() error {
	if owner.group == nil || owner.parent == nil || owner.groupRef == nil || owner.parentRef == nil {
		return imageNativeFailureV1("the original image scope has no retained native parent")
	}
	group, err := owner.group.Stat()
	if err != nil {
		return err
	}
	parent, err := owner.parent.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(group, owner.groupRef) || !os.SameFile(parent, owner.parentRef) {
		return imageNativeFailureV1("the original image scope or controlled parent changed native identity")
	}
	fd, err := unix.Openat(int(owner.group.Fd()), "..", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	actual := os.NewFile(uintptr(fd), "original-image-scope-actual-parent")
	info, err := actual.Stat()
	closeErr := actual.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	if !os.SameFile(info, owner.parentRef) {
		return imageNativeFailureV1("the original image scope moved from its captured controlled parent")
	}
	return nil
}

func (owner *imageNativeGuardianV1) ManagerCloseDiagnosticsV4() error {
	if owner == nil {
		return nil
	}
	return owner.managerDiagnostic
}
