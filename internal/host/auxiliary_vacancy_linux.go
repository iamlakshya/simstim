//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync"

	"github.com/godbus/dbus/v5"
)

// auxiliaryVacancyV1 retains native absence facts under one ORIGINAL owning
// OP and the same actual private root. It is not reconstructed from a PID,
// Life, Pulled, a saved socket or a diagnostic/accounting result. A complete
// auxiliary producer must additionally capture artifacts and freshly admit
// and own its NEW physical activation before it can obtain a boot receipt.
type auxiliaryVacancyV1 struct {
	stopped               *StoppedSessionV1
	stoppedOriginalHolder *sessionFileLease
	stoppedVacancyHolder  *sessionFileLease
	rewindFailed          *rewindFailedCreationV1
	operation             *SessionOperation
	holder                *sessionFileLease
	manager               *nativeUserManagerV1
	primary, auxiliary    string
	boot                  string
	need, runtime         []byte
	mu                    sync.Mutex
	transferred           bool
	reservation           *AuxiliaryReservationV1
	once                  sync.Once
	closeErr              error
	managerDiagnostic     error
}

func auxiliaryUnavailableV1(message string) error {
	return UserError("auxiliary_unavailable", message, "Keep the original stopped operation and resources; use the current native owner to finish or repair this sandbox before retrying.")
}

func captureAuxiliaryVacancyV1(ctx context.Context, operation *SessionOperation) (partial *auxiliaryVacancyV1, returned error) {
	if ctx == nil || operation == nil || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) {
		return nil, auxiliaryUnavailableV1("the auxiliary source has no original owning session operation")
	}
	if err := errors.Join(CheckCaller(), CheckStartContext(ctx), ctx.Err()); err != nil {
		return nil, err
	}
	snapshot, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	boot := BootID()
	if !auxiliaryBootIDV1(boot) || snapshot.ID != operation.Primary() || snapshot.SandboxOf != "" || snapshot.Wall == nil {
		return nil, auxiliaryUnavailableV1("the auxiliary source has no original protected sandbox or current native boot")
	}
	// A current-boot unmanaged legacy process could be outside the canonical
	// unit even when that unit is absent. Neither a lock nor that absence can
	// establish its whole lifetime. A changed real boot epoch cannot retain
	// any process from the old epoch; this is still no execution grant.
	if !auxiliaryBootIDV1(snapshot.Run.BootID) || snapshot.Run.BootID == boot {
		return nil, auxiliaryUnavailableV1("the original current-boot sandbox needs a genuine whole-lifetime stop owner")
	}
	if snapshot.Auxiliary != nil {
		return nil, auxiliaryUnavailableV1("the prior auxiliary reservation needs its original native settlement")
	}
	if err := ValidateManifestStart(snapshot); err != nil {
		return nil, err
	}
	holder, err := openOwnedSessionFileLease(ctx, operation.Primary(), holderLockFile, 0)
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, holder.Close())
		}
	}()
	if err := operation.native.rootRef.CheckRoot(holder.root); err != nil {
		return nil, err
	}
	manager, err := openNativeUserManagerV1(ctx)
	result := &auxiliaryVacancyV1{operation: operation, holder: holder, manager: manager, primary: UnitName(operation.Primary()), boot: boot, need: bytes.Clone(snapshot.VMNeedV1)}
	defer func() {
		if !complete {
			cleanup := result.Close()
			returned = errors.Join(returned, cleanup, result.CloseDiagnosticsV4())
			if cleanup != nil {
				partial = result
			}
		}
	}()
	if err != nil {
		return nil, errors.Join(auxiliaryUnavailableV1("whole-unit absence has no authenticated native manager lifetime"), err)
	}
	auxiliary, err := AuxUnitNameV1(operation.Primary())
	if err != nil {
		return nil, err
	}
	runtime, err := startupRuntimeBytes(snapshot)
	if err != nil {
		return nil, err
	}
	result.auxiliary = auxiliary
	result.runtime = bytes.Clone(runtime)
	if err := result.Check(ctx); err != nil {
		return nil, err
	}
	complete = true
	return result, nil
}

func (vacancy *auxiliaryVacancyV1) Check(ctx context.Context) error {
	if vacancy == nil || ctx == nil || vacancy.operation == nil || vacancy.holder == nil || vacancy.manager == nil {
		return auxiliaryUnavailableV1("the original native vacancy owner is absent")
	}
	vacancy.mu.Lock()
	defer vacancy.mu.Unlock()
	if vacancy.transferred {
		return auxiliaryUnavailableV1("the original native vacancy was already transferred to its activation")
	}
	if err := errors.Join(ctx.Err(), CheckCaller()); err != nil {
		return err
	}
	if !vacancy.operation.Valid(vacancy.operation.Primary()) || vacancy.operation.native.borrowed {
		return auxiliaryUnavailableV1("the original auxiliary operation ended")
	}
	if err := errors.Join(vacancy.holder.check(ctx), vacancy.operation.native.rootRef.CheckRoot(vacancy.holder.root), vacancy.manager.checkNativeManager()); err != nil {
		return err
	}
	if BootID() != vacancy.boot {
		return auxiliaryUnavailableV1("the native boot epoch changed during auxiliary capture")
	}
	if vacancy.rewindFailed != nil {
		if err := vacancy.rewindFailed.checkV1(ctx, vacancy.operation); err != nil {
			return err
		}
	}
	snapshot, err := vacancy.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	runtime, err := startupRuntimeBytes(snapshot)
	if err != nil {
		return err
	}
	if snapshot.ID != vacancy.operation.Primary() || snapshot.SandboxOf != "" || !bytes.Equal(snapshot.VMNeedV1, vacancy.need) || !bytes.Equal(runtime, vacancy.runtime) || !reflect.DeepEqual(snapshot.Auxiliary, vacancy.reservation) {
		return auxiliaryUnavailableV1("the protected original auxiliary source changed")
	}
	if err := vacancy.manager.requireAuxiliaryUnitAbsentV1(ctx, vacancy.primary); err != nil {
		return err
	}
	if err := vacancy.manager.requireAuxiliaryUnitAbsentV1(ctx, vacancy.auxiliary); err != nil {
		return err
	}
	ordinary, err := readMemoryUnitV1(ctx, vacancy.operation.Primary(), true)
	if err != nil {
		return err
	}
	auxiliary, err := readAuxMemoryUnitV1(ctx, vacancy.operation.Primary())
	if err != nil {
		return err
	}
	if ordinary.exists || auxiliary.exists {
		return auxiliaryUnavailableV1("an original ordinary or auxiliary native cgroup still exists")
	}
	// Recheck the actual manager AFTER the native cgroup observations. The
	// original OP stays held, so no product operation can race a new start.
	if err := vacancy.manager.requireAuxiliaryUnitAbsentV1(ctx, vacancy.primary); err != nil {
		return err
	}
	if err := vacancy.manager.requireAuxiliaryUnitAbsentV1(ctx, vacancy.auxiliary); err != nil {
		return err
	}
	return errors.Join(vacancy.holder.check(ctx), vacancy.manager.checkNativeManager(), ctx.Err())
}

// Only the authenticated direct manager peer can supply this negative result.
// Names are derived by the constructor; no manifest PID/unit path is queried.
func (manager *nativeUserManagerV1) requireAuxiliaryUnitAbsentV1(ctx context.Context, unit string) error {
	if manager == nil || ctx == nil {
		return auxiliaryUnavailableV1("native unit absence has no original manager")
	}
	if err := errors.Join(manager.checkNativeManager(), ctx.Err()); err != nil {
		return err
	}
	var path dbus.ObjectPath
	call := manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".GetUnit", 0, unit)
	err := call.Store(&path)
	absent := false
	if failure, ok := call.Err.(dbus.Error); ok {
		absent = failure.Name == "org.freedesktop.systemd1.NoSuchUnit"
	}
	if failure, ok := call.Err.(*dbus.Error); ok {
		absent = failure.Name == "org.freedesktop.systemd1.NoSuchUnit"
	}
	if check := manager.checkNativeManager(); check != nil {
		return errors.Join(err, check)
	}
	if !absent {
		return errors.Join(auxiliaryUnavailableV1("the actual ordinary or auxiliary native unit is not absent"), err)
	}
	return ctx.Err()
}
func (vacancy *auxiliaryVacancyV1) Close() error {
	if vacancy == nil {
		return nil
	}
	vacancy.once.Do(func() {
		vacancy.mu.Lock()
		defer vacancy.mu.Unlock()
		if vacancy.manager != nil {
			vacancy.closeErr = errors.Join(vacancy.closeErr, closeNativeManagerResourceV4(vacancy.manager, vacancy.operation.Primary(), &vacancy.managerDiagnostic))
		}
		if vacancy.holder != nil {
			vacancy.closeErr = errors.Join(vacancy.closeErr, vacancy.holder.Close())
		}
		clear(vacancy.need)
		clear(vacancy.runtime)
		vacancy.transferred = true
	})
	return vacancy.closeErr
}

func (vacancy *auxiliaryVacancyV1) CloseDiagnosticsV4() error {
	if vacancy == nil {
		return nil
	}
	return vacancy.managerDiagnostic
}
