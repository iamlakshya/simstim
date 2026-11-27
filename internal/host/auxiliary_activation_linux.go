//go:build linux

package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/iamlakshya/simstim/internal/brand"
	"golang.org/x/sys/unix"
)

const auxiliaryHelperModeV1 = "operation-guest-v1"

// startAuxiliaryNativeV1 dispatches ONE closed new native helper activation.
// Selection data, an old Need, a unit string and manager status alone cannot
// construct this owner: its actual original vacancy/artifact/reservation
// producer is required, and exact cap/native membership precede the OP loan.
func (owner *auxiliaryOperationV1) startAuxiliaryNativeV1(ctx context.Context) error {
	if owner == nil || owner.publication == nil || owner.vacancy == nil || owner.artifacts == nil || owner.ctx == nil {
		return auxiliaryUnavailableV1("the new auxiliary activation has no original native issuer")
	}
	if err := checkPurgeAuxiliaryAdmissionV1(ctx); err != nil {
		return err
	}
	owner.mu.Lock()
	used := owner.dispatched || owner.activation != nil || owner.closed
	owner.mu.Unlock()
	if used {
		return auxiliaryUnavailableV1("this original auxiliary activation was already used or closed")
	}
	if err := owner.vacancy.Check(ctx); err != nil {
		return err
	}
	if err := owner.artifacts.CheckBeforeActivation(ctx); err != nil {
		return err
	}
	manager := owner.vacancy.manager
	unit, err := AuxUnitNameV1(owner.operation.Primary())
	if err != nil {
		return err
	}
	if err := errors.Join(manager.checkNativeManager(), manager.events.arm(unit)); err != nil {
		return err
	}
	executable := owner.artifacts.placement.executable
	if _, err := mappedExecutableMatches(executable, owner.artifacts.helperIdentity); err != nil {
		return err
	}
	argv := []string{executable, auxiliaryHelperModeV1, owner.operation.Primary()}
	property := func(name string, value any) transientPropertyV1 {
		return transientPropertyV1{Name: name, Value: dbus.MakeVariant(value)}
	}
	properties := []transientPropertyV1{
		property("Description", brand.Name+" auxiliary operation "+owner.operation.Primary()),
		property("Type", "exec"), property("Slice", brand.Slice), property("KillMode", "mixed"),
		property("TimeoutStopUSec", uint64(30_000_000)), property("OOMPolicy", "continue"), property("Restart", "no"),
		property("CollectMode", "inactive-or-failed"), property("TasksMax", uint64(4096)),
		property("ExecStartEx", []transientExecV1{{Path: executable, Argv: slices.Clone(argv), Flags: []string{"no-env-expand"}}}),
		property("Environment", supervisorLaunchEnv(unit)), property("UnsetEnvironment", []string{holderLockFDEnv}),
		property("MemoryMax", uint64(owner.reservation.Declared)), property("MemorySwapMax", uint64(0)),
	}
	var call *dbus.Call
	err = owner.publication.gate(owner.ctx, func() error {
		if err := checkPurgeAuxiliaryAdmissionV1(owner.ctx); err != nil {
			return err
		}
		if err := checkAuxiliaryMemoryStartLockedV1(owner.ctx, owner.operation, &owner.reservation); err != nil {
			return err
		}
		manager.events.markDispatched()
		owner.mu.Lock()
		owner.dispatched = true
		owner.mu.Unlock()
		_ = manager.connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
		defer manager.connection.SetWriteDeadline(time.Time{})
		call = manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).GoWithContext(manager.ctx, systemdManagerInterface+".StartTransientUnit", 0, make(chan *dbus.Call, 1), unit, "fail", properties, []transientAuxV1{})
		owner.mu.Lock()
		owner.creation = call
		owner.mu.Unlock()
		return nil
	})
	if err != nil {
		return err
	}
	// The original actual native request retains its own lifetime even when the
	// caller cancels. A missing reply remains owned, never synthetic no-child.
	call = <-call.Done
	var job dbus.ObjectPath
	if err := call.Store(&job); err != nil {
		switch call.Err.(type) {
		case dbus.Error, *dbus.Error:
			manager.events.recordNegativeReply()
		}
		return errors.Join(err, manager.checkNativeManager(), manager.events.failure())
	}
	if !job.IsValid() || job == "/" {
		return auxiliaryUnavailableV1("the native manager returned no exact auxiliary startup job")
	}
	owner.mu.Lock()
	owner.job = job
	owner.mu.Unlock()
	if err := owner.resolveAuxiliaryActivationV1(ctx); err != nil {
		return err
	}
	if err := owner.checkAuxiliaryNativeMemoryV1(ctx); err != nil {
		return err
	}
	return owner.connectAuxiliaryV1(ctx)
}

// checkAuxiliaryNativeMemoryV1 brackets exact native membership and cap facts
// with the SAME observed new activation pidfd. This is required before any
// private artifact or OP/holder descriptor leaves the original outside owner.
func (owner *auxiliaryOperationV1) checkAuxiliaryNativeMemoryV1(ctx context.Context) error {
	if owner == nil || owner.activation == nil || owner.activation.peer == nil {
		return auxiliaryUnavailableV1("the auxiliary source has no exact created native helper")
	}
	if err := owner.checkAuxiliaryCreatedV1(ctx); err != nil {
		return err
	}
	data, err := owner.activation.peer.proc.ReadFile("cgroup")
	if err != nil {
		return err
	}
	unit, err := AuxUnitNameV1(owner.operation.Primary())
	if err != nil {
		return err
	}
	expected := fmt.Sprintf("0::/user.slice/user-%d.slice/user@%d.service/%s/%s\n", os.Getuid(), os.Getuid(), brand.Slice, unit)
	if string(data) != expected {
		return auxiliaryUnavailableV1("the actual auxiliary helper is outside its exact owned native cgroup")
	}
	native, err := readAuxMemoryUnitV1(ctx, owner.operation.Primary())
	if err != nil {
		return err
	}
	if !native.exists || native.maximum != owner.reservation.Declared || native.swap != 0 {
		return auxiliaryUnavailableV1("the actual auxiliary helper has no exact declared native cap and zero swap")
	}
	return owner.checkAuxiliaryCreatedV1(ctx)
}

// Resolve only the original returned creation job and first observed activation.
// It cannot adopt a later same-name native unit or a saved process number.
func (owner *auxiliaryOperationV1) resolveAuxiliaryActivationV1(ctx context.Context) error {
	manager := owner.vacancy.manager
	unit, err := AuxUnitNameV1(owner.operation.Primary())
	if err != nil {
		return err
	}
	owner.mu.Lock()
	job := owner.job
	owner.mu.Unlock()
	if !job.IsValid() || job == "/" {
		return auxiliaryUnavailableV1("the actual auxiliary dispatch has no original creation job")
	}
	path, err := manager.events.waitJob(ctx, job)
	if err != nil {
		return err
	}
	observed, err := manager.activation(ctx, path, unit)
	if err != nil {
		return err
	}
	if err := manager.events.bindActivation(observed); err != nil {
		return err
	}
	pidfd, err := unix.PidfdOpen(int(observed.MainPID), 0)
	if err != nil {
		return err
	}
	proc, err := os.OpenRoot(fmt.Sprintf("/proc/%d", observed.MainPID))
	if err != nil {
		return errors.Join(err, unix.Close(pidfd))
	}
	activation := &nativeUnitActivationV1{manager: manager, path: path, unit: unit, properties: observed,
		peer:       &nativeStartupPeerV1{pid: int(observed.MainPID), uid: uint32(os.Getuid()), pidfd: pidfd, proc: proc},
		executable: owner.artifacts.helperIdentity, argv: []string{owner.artifacts.placement.executable, auxiliaryHelperModeV1, owner.operation.Primary()}, memoryMax: owner.reservation.Declared}
	owner.mu.Lock()
	owner.activation = activation
	owner.mu.Unlock()
	if err := activation.checkCreationV1(ctx); err != nil {
		return err
	}
	return nil
}

func (owner *auxiliaryOperationV1) checkAuxiliaryCreatedV1(ctx context.Context) error {
	if owner.activation.peer.identity == nil {
		return owner.activation.checkCreationV1(ctx)
	}
	return owner.activation.Check(ctx)
}
