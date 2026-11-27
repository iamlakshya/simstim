//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/godbus/dbus/v5"
)

// startCustodySupervisorV1 installs the creation-time custody properties and
// passive pin before the original native main exists. StartUnit owns the same
// loaded runtime service on both initial creation and future reactivation.
func (manager *nativeUserManagerV1) startCustodySupervisorV1(ctx context.Context, m *Manifest, executable string, argv []string, admission *runtimePublication) (result *nativeUnitActivationV1, failure error) {
	info, err := os.Stat(executable)
	if err != nil {
		return nil, err
	}
	runtime, err := manager.installCustodyRuntimeV1(ctx, m, executable, argv, admission)
	if err != nil {
		if runtime != nil {
			return &nativeUnitActivationV1{manager: manager, unit: UnitName(m.ID), runtime: runtime}, err
		}
		return nil, err
	}
	complete := false
	var path dbus.ObjectPath
	defer func() {
		if !complete {
			// Retain the actual loaded runtime after any dispatch, including
			// a reply/watcher/creation failure before a pidfd could be bound.
			if manager.events.wasDispatched() {
				result = &nativeUnitActivationV1{manager: manager, path: path, unit: UnitName(m.ID), runtime: runtime, executable: info, argv: append([]string(nil), argv...), memoryMax: m.Limits.MemoryMax}
				return
			}
			cleanup := runtime.Close()
			failure = errors.Join(failure, cleanup)
			if cleanup != nil {
				result = &nativeUnitActivationV1{manager: manager, path: path, unit: UnitName(m.ID), runtime: runtime}
			}
		}
	}()
	if err := manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".GetUnit", 0, UnitName(m.ID)).Store(&path); err != nil {
		return nil, err
	}
	if err := manager.events.armLoadedV1(UnitName(m.ID), path, ""); err != nil {
		return nil, err
	}
	var call *dbus.Call
	err = admission.gateControl(ctx, func(control *placementControl) error {
		if err := checkStartingMemoryControlLockedV1(ctx, admission.startup, m, control); err != nil {
			return err
		}
		if err := errors.Join(runtime.service.check(), runtime.pin.check(), manager.checkCustodyPinV1(ctx, UnitName(m.ID))); err != nil {
			return err
		}
		if err := consumeColdResumeDispatchLockedV1(ctx, admission.startup, admission); err != nil {
			return err
		}
		runtime.mu.Lock()
		runtime.pendingInstall = false // Actual StartUnit may now create a main.
		runtime.mu.Unlock()
		manager.events.markDispatched()
		_ = manager.connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
		defer manager.connection.SetWriteDeadline(time.Time{})
		call = manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).GoWithContext(manager.ctx, systemdManagerInterface+".StartUnit", 0, make(chan *dbus.Call, 1), UnitName(m.ID), "fail")
		return nil
	})
	if err != nil {
		return nil, err
	}
	call = <-call.Done
	var job dbus.ObjectPath
	if err := call.Store(&job); err != nil {
		switch call.Err.(type) {
		case dbus.Error, *dbus.Error:
			manager.events.recordNegativeReply()
		}
		return nil, errors.Join(err, manager.checkNativeManager(), manager.events.failure())
	}
	if !job.IsValid() || job == "/" {
		return nil, sessionLeaseError("the runtime custody service has no exact native startup job")
	}
	activation, err := manager.finishSupervisorActivationV1(ctx, m, info, argv, job, 0)
	if activation != nil {
		activation.runtime = runtime
		complete = true
	}
	if err != nil {
		return activation, err
	}
	return activation, nil
}
