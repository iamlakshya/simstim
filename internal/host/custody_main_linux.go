//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"golang.org/x/sys/unix"
)

const custodyBundleSizeV1 = 5

// ManagerCustodyMainV1 retains capabilities of the actual original service
// main. It is a pre-deposit owner, never a stopped/cold/execute Source. Only a
// committed OwnedSupervisorLease can construct it; no FD/JSON setter exists.
type ManagerCustodyMainV1 struct {
	mu                          sync.Mutex
	owner                       *OwnedSupervisorLease
	manager                     *nativeUserManagerV1
	path                        dbus.ObjectPath
	activation                  unitActivationProperties
	root, holder, cgroup, pidfd *os.File
	claim                       *custodyClaimV1
	runtime                     *custodyRuntimeV1
	rootRef                     cachefs.Reference
	deposited                   bool
	closed                      bool
	retainedStop                bool
	closeErr                    error
	managerDiagnostic           error
	retirement                  nativeStartupRetirementsV1
}

// PrepareManagerCustodyV1 captures only the current committed main's original
// native objects. A non-custody legacy unit remains on its unchanged lifetime
// path. Custody-capable units are verified through the actual held manager.
func (owner *OwnedSupervisorLease) PrepareManagerCustodyV1(ctx context.Context) (result *ManagerCustodyMainV1, returned error) {
	if owner == nil || owner.native == nil || ctx == nil || !owner.Valid(owner.native.id) {
		return nil, sessionLeaseError("custody preparation has no original live supervisor owner")
	}
	// A managerless creation keeps its existing live/None path. This absence
	// creates no stopped or cold authority and never issues a VM lease.
	if os.Getenv(unitEnv) == "" {
		return nil, nil
	}
	manager, err := openNativeUserManagerV1(ctx)
	if manager == nil {
		return nil, err
	}
	main := &ManagerCustodyMainV1{owner: owner, manager: manager}
	complete := false
	defer func() {
		if !complete {
			cleanup := main.Close()
			returned = errors.Join(returned, cleanup, main.managerDiagnostic, main.retirement.diagnosticsV4(), main.claim.closeDiagnosticsV1(), main.runtime.CloseDiagnosticsV4())
			if cleanup != nil {
				result = main
			}
		}

	}()
	if err != nil {
		return nil, err
	}
	if err := manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".GetUnitByPID", 0, uint32(os.Getpid())).Store(&main.path); err != nil {
		return nil, err
	}
	main.activation, err = manager.activation(ctx, main.path, UnitName(owner.native.id))
	if err != nil || main.activation.MainPID != uint32(os.Getpid()) {
		return nil, errors.Join(sessionLeaseError("custody producer is not the original native service main"), err)
	}
	enabled, err := manager.custodyPropertiesV1(ctx, main.path)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, nil
	}
	if err := owner.custodyOriginV1(ctx); err != nil {
		return nil, err
	}
	main.root, err = owner.native.root.Open(".")
	if err != nil {
		return nil, err
	}
	main.rootRef = owner.native.rootRef
	main.holder, err = duplicateAuxiliaryHolderV1(ctx, owner.native)
	if err != nil {
		return nil, err
	}
	main.cgroup, err = openNativeStopCgroupV1(owner.native.id)
	if err != nil {
		return nil, err
	}
	// This is the same still-running producer opening its own process, not
	// recovery of a saved PID or an outside process lookup.
	descriptor, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		return nil, err
	}
	main.pidfd = os.NewFile(uintptr(descriptor), "original-custody-main-pidfd")
	main.claim, err = newCustodyClaimV1()
	if err != nil {
		return nil, err
	}
	if err := main.checkLiveV1(ctx); err != nil {
		return nil, err
	}
	if err := manager.checkCustodyPinV1(ctx, UnitName(owner.native.id)); err != nil {
		return nil, err
	}
	main.runtime, err = manager.captureCustodyRuntimeV1(ctx, main.path, owner.native.id)
	if err != nil || main.runtime == nil {
		return nil, errors.Join(sessionLeaseError("the original custody main has no exact owned runtime configuration"), err)
	}
	if err := owner.bindManagerStopSourceV1(ctx, main); err != nil {
		return nil, err
	}
	complete = true
	return main, nil
}

func (manager *nativeUserManagerV1) custodyPropertiesV1(ctx context.Context, path dbus.ObjectPath) (bool, error) {
	maximum, err := manager.getProperty(ctx, path, systemdServiceInterface, "FileDescriptorStoreMax")
	if err != nil {
		return false, err
	}
	count, ok := maximum.Value().(uint32)
	if !ok || count == 0 {
		return false, nil
	}
	if count != custodyBundleSizeV1 {
		return false, sessionLeaseError("the service custody role bound differs from the closed native bundle")
	}
	for _, property := range []struct{ name, want string }{{"FileDescriptorStorePreserve", "yes"}, {"NotifyAccess", "main"}, {"Restart", "no"}} {
		value, err := manager.getProperty(ctx, path, systemdServiceInterface, property.name)
		if err != nil {
			return false, err
		}
		if actual, ok := value.Value().(string); !ok || actual != property.want {
			return false, sessionLeaseError("the original custody service lost its closed native properties")
		}
	}
	return true, nil
}

func (main *ManagerCustodyMainV1) checkLiveV1(ctx context.Context) error {
	if main == nil || main.closed || main.owner == nil || main.manager == nil || main.owner.native == nil {
		return sessionLeaseError("the original manager custody main ended")
	}
	if err := errors.Join(main.owner.custodyOriginV1(ctx), main.manager.checkNativeManager(), main.owner.native.check(ctx)); err != nil {
		return err
	}
	original, err := main.owner.native.root.Stat(".")
	if err != nil {
		return err
	}
	actual, err := main.root.Stat()
	if err != nil || !os.SameFile(original, actual) {
		return errors.Join(sessionLeaseError("the original custody root descriptor changed"), err)
	}
	if err := main.owner.native.fileRef.CheckFile(main.owner.native.root, holderLockFile, main.holder, 0o600); err != nil {
		return err
	}
	if err := main.rootRef.CheckRoot(main.owner.native.root); err != nil {
		return err
	}
	current, err := main.manager.activation(ctx, main.path, UnitName(main.owner.native.id))
	if err != nil || !sameUnitActivationProperties(current, main.activation) || current.MainPID != uint32(os.Getpid()) {
		return errors.Join(sessionLeaseError("the original custody main invocation changed"), err)
	}
	if main.claim == nil {
		return sessionLeaseError("the original custody main lost its one-claim kernel object")
	}
	return main.claim.peek()
}

func (owner *OwnedSupervisorLease) custodyOriginV1(ctx context.Context) error {
	if owner == nil || ctx == nil || owner.native == nil {
		return sessionLeaseError("manager custody has no actual supervisor owner")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	boot := owner.startup
	if !owner.Valid(owner.native.id) || boot == nil || boot.owner != owner || owner.native.borrowed {
		return sessionLeaseError("manager custody has no original native boot producer")
	}
	if !boot.ready {
		if boot.operation == nil || !boot.operation.Valid(owner.native.id) || owner.pending == nil || owner.pending.peer == nil {
			return sessionLeaseError("the initial custody preparation lost its actual startup loan")
		}
		if err := owner.pending.peer.Check(); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// Close releases the main's local copies without unlocking shared OFDs or
// clearing the manager's store. The verified manager custodian owns its copies.
func (main *ManagerCustodyMainV1) Close() error {
	if main == nil {
		return nil
	}
	main.mu.Lock()
	defer main.mu.Unlock()
	if main.closed {
		return main.closeErr
	}
	files := []*os.File{main.root, main.holder, main.cgroup, main.pidfd}
	_ = main.retirement.closeFilesV4(files)
	manager := closeNativeManagerResourceV4(main.manager, main.owner.native.id, &main.managerDiagnostic)
	main.closeErr = errors.Join(main.retirement.checkFilesV4(files), main.claim.Close(), main.runtime.Close(), manager)
	if main.closeErr == nil {
		main.closed = true
	}
	return main.closeErr
}

// RetainForSameRequestStopV1 selects only the actual original main's ordinary
// retained-stop path. Its caller already owns the real native stop scope.
// This changes no stopped predicate, mints no receipt and deposits no bundle.
func (main *ManagerCustodyMainV1) RetainForSameRequestStopV1(ctx context.Context, owner *OwnedSupervisorLease) error {
	if main == nil || owner == nil || main.owner != owner {
		return sessionLeaseError("the retained stop has another actual custody main")
	}
	main.mu.Lock()
	defer main.mu.Unlock()
	if err := main.checkLiveV1(ctx); err != nil {
		return err
	}
	if main.deposited || main.retainedStop {
		return sessionLeaseError("the original custody main already settled a stop purpose")
	}
	main.retainedStop = true
	return nil
}
