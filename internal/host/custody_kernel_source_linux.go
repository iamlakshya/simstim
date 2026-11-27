//go:build linux

package host

import (
	"context"
	"encoding/hex"
	"errors"
	"os"

	"github.com/godbus/dbus/v5"
)

// The common substrate retains actual kernel objects only. Walled cold and
// managed None have distinct issuers, accounting, final consumers and locks.
// No manifest/FD setter can turn this substrate into either source class.
type custodyKernelSourceV1 struct {
	operation                  *SessionOperation
	origin                     *placement
	manager                    *nativeUserManagerV1
	path                       dbus.ObjectPath
	oldInvocation              string
	boot                       string
	source                     Manifest
	runtime                    *custodyRuntimeV1
	launch                     *startupLaunchV1
	roles                      map[string]*os.File
	roleClosures               map[string]*custodyAliasRetirementV1
	group, creation            *os.File
	dormantGroup               *os.File
	managerDiagnostic          error
	claimed, cancelled, closed bool
}

// Only concrete current-operation producers call this native capture. It
// neither starts a helper nor creates any vacancy, VM artefact or reservation.
func captureCustodyKernelSourceV1(ctx context.Context, operation *SessionOperation, m *Manifest) (result *custodyKernelSourceV1, failure error) {
	if operation == nil || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) || m == nil || m.ID != operation.Primary() || m.SandboxOf != "" || m.Auxiliary != nil || m.Run.BootID != BootID() {
		return nil, sessionLeaseError("custody kernel capture has no exact current outside operation")
	}
	origin, err := placementForCall()
	if err != nil {
		return nil, err
	}
	if err := errors.Join(CheckCaller(), origin.checkIdentity()); err != nil {
		return nil, err
	}
	manager, err := openNativeUserManagerV1(ctx)
	if manager == nil {
		return nil, err
	}
	source := &custodyKernelSourceV1{operation: operation, origin: origin, manager: manager, boot: BootID(), source: CloneManifest(*m), roles: make(map[string]*os.File, custodyBundleSizeV1)}
	complete := false
	defer func() {
		if !complete {
			cleanup := source.closeKernelV1()
			failure = errors.Join(failure, cleanup, source.managerDiagnostic, source.runtime.CloseDiagnosticsV4())
			if cleanup != nil {
				result = source
			}
		}
	}()
	if err != nil {
		return nil, err
	}
	if err := manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".GetUnit", 0, UnitName(m.ID)).Store(&source.path); err != nil {
		return nil, err
	}
	if err := source.checkPinnedV1(ctx); err != nil {
		return nil, err
	}
	value, err := manager.getProperty(ctx, source.path, systemdUnitInterface, "InvocationID")
	if err != nil {
		return nil, err
	}
	invocation, ok := value.Value().([]byte)
	if !ok || len(invocation) != 16 {
		return nil, sessionLeaseError("the retained service has no original native invocation")
	}
	source.oldInvocation = hex.EncodeToString(invocation)
	source.runtime, err = manager.captureCustodyRuntimeV1(ctx, source.path, m.ID)
	if err != nil || source.runtime == nil {
		return nil, errors.Join(sessionLeaseError("the retained native service has no owned runtime configuration"), err)
	}
	if err := source.captureDormantGroupV1(ctx); err != nil {
		return nil, err
	}
	complete = true
	return source, nil
}

// Callers must first join their concrete native creation/claim owner. This
// method only closes the retained local aliases and never cleans a unit.
func (source *custodyKernelSourceV1) closeKernelV1() error {
	if source == nil {
		return nil
	}
	if err := source.closeProofAliasesV1(); err != nil {
		return err
	}
	for _, entry := range []struct {
		name string
		file **os.File
	}{{"bootstrap-group-observer", &source.group}, {"bootstrap-process-observer", &source.creation}, {"dormant-group-observer", &source.dormantGroup}} {
		if *entry.file == nil {
			continue
		}
		if err := source.retireAliasFileV1(entry.name, *entry.file); err != nil {
			return err
		}
		*entry.file = nil
	}
	if err := source.runtime.Close(); err != nil {
		return err
	}
	return closeNativeManagerResourceV4(source.manager, source.operation.Primary(), &source.managerDiagnostic)
}
