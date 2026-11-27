//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

const systemdPurgeScopeInterfaceV3 = "org.freedesktop.systemd1.Scope"

func (scope *purgeAttemptScopeV3) captureOriginalUnitV3(ctx context.Context) error {
	if err := scope.events.checkOriginalJobV3(scope.job); err != nil {
		return err
	}
	scope.events.mu.Lock()
	path, removed := scope.events.path, scope.events.removed
	scope.events.mu.Unlock()
	if removed {
		return placementError("the original purge scope ended before native adoption")
	}
	var actual dbus.ObjectPath
	if err := scope.manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".GetUnit", 0, scope.unit).Store(&actual); err != nil {
		return err
	}
	if actual != path {
		return placementError("the original purge job has another native unit")
	}
	for _, item := range []struct{ iface, name, want string }{{systemdUnitInterface, "Id", scope.unit}, {systemdPurgeScopeInterfaceV3, "Slice", "app.slice"}, {systemdPurgeScopeInterfaceV3, "ControlGroup", scope.groupPath}} {
		value, err := scope.manager.getProperty(ctx, path, item.iface, item.name)
		if err != nil {
			return err
		}
		text, ok := value.Value().(string)
		if !ok || text != item.want {
			return placementError("the original purge scope has another native unit boundary")
		}
	}
	transient, err := scope.manager.getProperty(ctx, path, systemdUnitInterface, "Transient")
	if err != nil {
		return err
	}
	isTransient, ok := transient.Value().(bool)
	if !ok || !isTransient {
		return placementError("the purge scope is not its actual original transient creation")
	}
	value, err := scope.manager.getProperty(ctx, path, systemdUnitInterface, "InvocationID")
	if err != nil {
		return err
	}
	invocation, ok := value.Value().([]byte)
	if !ok || len(invocation) != 16 {
		return placementError("the original purge scope has no exact manager invocation")
	}
	copy(scope.invocation[:], invocation)
	if scope.invocation == [16]byte{} {
		return placementError("the original purge scope has no native invocation generation")
	}
	scope.group, err = scope.openGroupV3()
	if err != nil {
		return err
	}
	scope.groupInfo, err = scope.group.Stat()
	return errors.Join(err, scope.manager.checkNativeManager())
}

func (scope *purgeAttemptScopeV3) openGroupV3() (*os.File, error) {
	root, err := unix.Open("/sys/fs/cgroup", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(root)
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(root, &filesystem); err != nil || filesystem.Type != unix.CGROUP2_SUPER_MAGIC {
		return nil, errors.Join(placementError("the original purge scope has no native unified cgroup root"), err)
	}
	fd, err := unix.Openat2(root, strings.TrimPrefix(scope.groupPath, "/"), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "actual-original-purge-scope-cgroup"), nil
}

func (scope *purgeAttemptScopeV3) checkGenerationV3(ctx context.Context) error {
	if scope == nil || ctx == nil || scope.owner == nil || scope.owner.scope != scope || scope.closed || scope.manager == nil || scope.job == nil || scope.group == nil || scope.groupInfo == nil || scope.invocation == [16]byte{} {
		return placementError("the original purge scope lost its native creation objects")
	}
	if err := errors.Join(ctx.Err(), scope.manager.checkNativeManager(), scope.events.checkOriginalJobV3(scope.job), scope.events.checkOnlyOriginalJobsV3(scope.job, scope.stopJob)); err != nil {
		return err
	}
	scope.events.mu.Lock()
	path, removed := scope.events.path, scope.events.removed
	scope.events.mu.Unlock()
	if removed {
		return placementError("the original purge scope was removed or replaced")
	}
	var actual dbus.ObjectPath
	if err := scope.manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".GetUnit", 0, scope.unit).Store(&actual); err != nil {
		return err
	}
	if actual != path {
		return placementError("the current scope name has another original manager object")
	}
	value, err := scope.manager.getProperty(ctx, path, systemdUnitInterface, "InvocationID")
	if err != nil {
		return err
	}
	invocation, ok := value.Value().([]byte)
	if !ok || !bytes.Equal(invocation, scope.invocation[:]) {
		return placementError("the original purge scope invocation was replaced")
	}
	value, err = scope.manager.getProperty(ctx, path, systemdPurgeScopeInterfaceV3, "ControlGroup")
	if err != nil {
		return err
	}
	group, ok := value.Value().(string)
	if !ok || group != scope.groupPath {
		return placementError("the original purge scope changed its native cgroup")
	}
	return scope.checkOriginalGroupV3()
}

func (scope *purgeAttemptScopeV3) checkOriginalGroupV3() error {
	info, err := scope.group.Stat()
	if err != nil || !os.SameFile(info, scope.groupInfo) {
		return errors.Join(placementError("the original purge cgroup descriptor changed"), err)
	}
	_, _, _, links, known := footprintIdentity(info)
	if !known {
		return placementError("the original purge cgroup has unknown native link state")
	}
	fresh, err := scope.openGroupV3()
	if errors.Is(err, os.ErrNotExist) && links == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	actual, statErr := fresh.Stat()
	closeErr := fresh.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(actual, scope.groupInfo) {
		return errors.Join(placementError("the original purge cgroup was replaced"), statErr, closeErr)
	}
	return nil
}

func (scope *purgeAttemptScopeV3) checkMembershipV3(ctx context.Context) error {
	owner := scope.owner
	if owner == nil || owner.peer == nil || owner.process == nil || owner.peer.pid != owner.process.Pid || scope.group == nil {
		return placementError("the original scope has no same actual child membership")
	}
	if err := errors.Join(ctx.Err(), scope.manager.checkNativeManager(), owner.peer.checkLive(), scope.checkOriginalGroupV3()); err != nil {
		return err
	}
	raw, err := owner.peer.proc.ReadFile("cgroup")
	if err != nil || !bytes.Equal(raw, []byte("0::"+scope.groupPath+"\n")) {
		return errors.Join(placementError("the actual copied child is not in its original outside-session scope"), err)
	}
	paths, below := cgroupPaths(raw)
	if !below || len(paths) != 1 || placeAmong(paths, "") != placeOutside {
		return errCallerInside
	}
	fd, err := unix.Openat2(int(scope.group.Fd()), "cgroup.procs", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "original-purge-scope-members")
	raw, readErr := io.ReadAll(io.LimitReader(file, 1<<20+1))
	closed := file.Close()
	if readErr != nil || closed != nil || len(raw) > 1<<20 {
		return errors.Join(placementError("the original scope membership is unavailable or oversized"), readErr, closed)
	}
	seen := false
	for _, line := range strings.Fields(string(raw)) {
		pid, err := strconv.Atoi(line)
		if err != nil || pid <= 0 {
			return placementError("the original scope returned unknown native membership")
		}
		if pid == owner.process.Pid {
			if seen {
				return placementError("the original scope duplicated its native child membership")
			}
			seen = true
		}
	}
	if !seen {
		return placementError("the original child is absent from its actual native cgroup")
	}
	return errors.Join(owner.peer.checkLive(), scope.manager.checkNativeManager())
}

func (scope *purgeAttemptScopeV3) checkLiveV3(ctx context.Context) error {
	if scope == nil || !scope.adopted || !scope.notified || scope.negative {
		return placementError("the original copied child has no verified live manager adoption")
	}
	if err := scope.checkGenerationV3(ctx); err != nil {
		return err
	}
	return errors.Join(scope.checkActiveV3(ctx), scope.checkMembershipV3(ctx))
}

func nativePurgeCreationExitedV3(owner *purgeAttemptExecV1) error {
	if owner == nil || owner.creation == nil || owner.creationFD < 0 || int(owner.creation.Fd()) != owner.creationFD {
		return placementError("the original purge creation lost its actual exit handle")
	}
	polls := []unix.PollFd{{Fd: int32(owner.creation.Fd()), Events: unix.POLLIN}}
	n, err := unix.Poll(polls, 0)
	if err != nil || n != 1 || polls[0].Revents&unix.POLLIN == 0 {
		return errors.Join(placementError("the original purge child has no positive native exit"), err)
	}
	return nil
}

func (scope *purgeAttemptScopeV3) closeAfterCreationV3() error {
	if scope == nil {
		return nil
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	if scope.closed {
		return errors.Join(scope.checkNativeRetiredV3(context.Background()), scope.CloseDiagnosticsV3())
	}
	if scope.closure != nil {
		return scope.closure.CloseV3()
	}
	owner := scope.owner
	if owner == nil || owner.scope != scope {
		return placementError("the purge scope cleanup changed its original owner")
	}
	if owner.process == nil {
		return scope.closeUnstartedV3()
	}
	if err := errors.Join(scope.checkTerminalForReapV3(), owner.checkJoinedCreationV2(), nativePurgeCreationExitedV3(owner)); err != nil {
		return err
	}
	if !scope.dispatched {
		return scope.closePhysicalV3()
	}
	if scope.stopCall != nil {
		if err := scope.settleOriginalStopV3(); err != nil {
			return err
		}
	}
	if err := scope.events.checkOnlyOriginalJobsV3(scope.job, scope.stopJob); err != nil {
		return err
	}
	if scope.group != nil {
		if err := scope.checkOriginalGroupV3(); err != nil {
			return err
		}
		empty, err := originalStopCgroupEmptyV1(scope.group)
		if err != nil || !empty {
			return errors.Join(placementError("the original purge scope still has native processes; preserve it"), err)
		}
	}
	if err := scope.requireAbsentV3(scope.manager.ctx); err != nil {
		if scope.group == nil {
			return err
		}
		if scope.stopCall == nil {
			if err := scope.checkGenerationV3(scope.manager.ctx); err != nil {
				return err
			}
			scope.stopCall = scope.manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).GoWithContext(scope.manager.ctx, systemdManagerInterface+".StopUnit", 0, make(chan *dbus.Call, 1), scope.unit, "fail")
			scope.stopReply = <-scope.stopCall.Done
			if err := scope.settleOriginalStopV3(); err != nil {
				return err
			}
		}
		if scope.stopReply != scope.stopCall || scope.stopJob == nil || scope.stopJob.result != "done" {
			return placementError("the original empty scope stop has unknown actual job closure")
		}
		if err := scope.events.checkOriginalJobV3(scope.stopJob); err != nil {
			return err
		}
		if err := scope.requireAbsentV3(scope.manager.ctx); err != nil {
			return err
		}
	}
	closure, err := newPurgeAttemptScopeClosureV3(scope)
	if err != nil {
		return err
	}
	scope.closure = closure
	return closure.CloseV3()
}

func (scope *purgeAttemptScopeV3) closePhysicalV3() error {
	if scope.closure == nil {
		closure, err := newUnusedPurgeScopeClosureV4(scope)
		if err != nil {
			return err
		}
		scope.closure = closure
	}
	return scope.closure.CloseV3()
}

func (scope *purgeAttemptScopeV3) settleOriginalStopV3() error {
	if scope.stopCall == nil || scope.stopReply != scope.stopCall {
		return placementError("the original empty scope stop call remains unknown")
	}
	var path dbus.ObjectPath
	if err := scope.stopCall.Store(&path); err != nil {
		return err
	}
	if scope.stopJob == nil {
		job, err := scope.events.waitOriginalJobV3(scope.manager.ctx, path)
		if err != nil {
			return err
		}
		scope.stopJob = job
	}
	if scope.stopJob.path != path || scope.stopJob.result != "done" {
		return placementError("the original empty scope stop has no completed actual job")
	}
	return errors.Join(scope.events.checkOriginalJobV3(scope.stopJob), scope.events.checkOnlyOriginalJobsV3(scope.job, scope.stopJob))
}

func (scope *purgeAttemptScopeV3) checkActiveV3(ctx context.Context) error {
	scope.events.mu.Lock()
	path, removed := scope.events.path, scope.events.removed
	scope.events.mu.Unlock()
	if removed {
		return placementError("the original purge scope is no longer active")
	}
	for _, item := range []struct{ iface, name, want string }{{systemdUnitInterface, "ActiveState", "active"}, {systemdUnitInterface, "SubState", "running"}, {systemdPurgeScopeInterfaceV3, "Result", "success"}, {systemdPurgeScopeInterfaceV3, "Controller", ""}} {
		value, err := scope.manager.getProperty(ctx, path, item.iface, item.name)
		if err != nil {
			return err
		}
		actual, ok := value.Value().(string)
		if !ok || actual != item.want {
			return placementError("the original removal scope has no active native lifetime")
		}
	}
	return nil
}
