//go:build linux

package host

import (
	"context"
	"errors"
	"os"

	"github.com/godbus/dbus/v5"
)

func (owner *OwnedSupervisorLease) checkOriginalTransientManagerStopV1(ctx context.Context) error {
	owner.mu.Lock()
	boot := owner.startup
	valid := owner.native != nil && !owner.native.borrowed && owner.Valid(owner.native.id) && boot != nil && boot.owner == owner && boot.ready && owner.managerStop == nil
	owner.mu.Unlock()
	if !valid {
		return sessionLeaseError("the retained stop has no original committed transient producer")
	}
	if err := owner.native.check(ctx); err != nil {
		return err
	}
	manager, err := openNativeUserManagerV1(ctx)
	if err != nil {
		return err
	}
	defer manager.Close()
	var path dbus.ObjectPath
	if err := manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".GetUnitByPID", 0, uint32(os.Getpid())).Store(&path); err != nil {
		return err
	}
	activation, err := manager.activation(ctx, path, UnitName(owner.native.id))
	if err != nil || activation.MainPID != uint32(os.Getpid()) {
		return errors.Join(sessionLeaseError("the retained stop lost its actual original native main"), err)
	}
	transient, err := manager.getProperty(ctx, path, systemdUnitInterface, "Transient")
	if err != nil {
		return err
	}
	if original, ok := transient.Value().(bool); !ok || !original {
		return sessionLeaseError("a persistent or unknown manager runtime has no bound original stop owner")
	}
	for _, name := range []string{"FileDescriptorStoreMax", "NFileDescriptorStore"} {
		value, err := manager.getProperty(ctx, path, systemdServiceInterface, name)
		if err != nil {
			return err
		}
		if count, ok := value.Value().(uint32); !ok || count != 0 {
			return sessionLeaseError("an actual manager custody runtime cannot use transient stop settlement")
		}
	}
	owner.mu.Lock()
	same := owner.startup == boot && boot.ready && owner.managerStop == nil && owner.Valid(owner.native.id)
	owner.mu.Unlock()
	if !same {
		return sessionLeaseError("the original transient manager stop owner changed")
	}
	return errors.Join(ctx.Err(), owner.native.check(ctx), manager.checkNativeManager())
}
