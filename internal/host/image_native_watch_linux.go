//go:build linux

package host

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

// A reference on the original direct manager connection keeps the actual
// operation's unit referenced while native vacancy is observed. It remains
// corroboration, never a replacement for the original pinned cgroup events.
func (owner *imageNativeGuardianV1) retainManagerScopeV1(ctx context.Context, scope imageScopeInfo) error {
	manager, err := openNativeUserManagerV1(ctx)
	owner.manager = manager // Adopt cleanup-only partial objects before errors.
	if err != nil {
		return err
	}
	var unitPath dbus.ObjectPath
	if err := manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".GetUnit", 0, owner.unit).Store(&unitPath); err != nil {
		return err
	}
	if !unitPath.IsValid() || unitPath == "/" {
		return imageNativeFailureV1("the original native manager returned no actual image scope")
	}
	id, err := manager.getProperty(ctx, unitPath, systemdUnitInterface, "Id")
	if err != nil {
		return err
	}
	active, err := manager.getProperty(ctx, unitPath, systemdUnitInterface, "ActiveState")
	if err != nil {
		return err
	}
	invocation, err := manager.getProperty(ctx, unitPath, systemdUnitInterface, "InvocationID")
	if err != nil {
		return err
	}
	group, err := manager.getProperty(ctx, unitPath, "org.freedesktop.systemd1.Scope", "ControlGroup")
	if err != nil {
		return err
	}
	actualID, okID := id.Value().(string)
	actualActive, okActive := active.Value().(string)
	actualInvocation, okInvocation := invocation.Value().([]byte)
	actualGroup, okGroup := group.Value().(string)
	if !okID || actualID != owner.unit || !okActive || actualActive != "active" || !okInvocation || len(actualInvocation) != 16 || hex.EncodeToString(actualInvocation) != scope.InvocationID || !okGroup || actualGroup != scope.ControlGroup {
		return imageNativeFailureV1("the original native manager does not own the actual private guardian scope")
	}
	if err := manager.checkNativeManager(); err != nil {
		return err
	}
	if err := manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".RefUnit", 0, owner.unit).Err; err != nil {
		return err
	}
	owner.unitPath = unitPath
	return manager.checkNativeManager()
}
func (owner *imageNativeGuardianV1) watchVacancyV1() {
	defer close(owner.watchDone)
	for {
		select {
		case <-owner.watchStop:
			return
		default:
		}
		empty, err := readImageScopeVacancyV1(owner.events)
		owner.mu.Lock()
		if err != nil && owner.watchErr == nil {
			owner.watchErr = err
		}
		if err == nil && empty {
			owner.vacant = true
			owner.mu.Unlock()
			return
		}
		owner.mu.Unlock()
		descriptors := []unix.PollFd{{Fd: int32(owner.events.Fd()), Events: unix.POLLPRI | unix.POLLERR}}
		_, err = unix.Poll(descriptors, 100)
		if err != nil && !errors.Is(err, unix.EINTR) {
			owner.mu.Lock()
			if owner.watchErr == nil {
				owner.watchErr = err
			}
			owner.mu.Unlock()
		}
	}
}
