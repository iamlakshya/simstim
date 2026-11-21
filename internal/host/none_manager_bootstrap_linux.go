//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

func (cold *NoneColdResumeV1) prepareManagerBootstrapV1(ctx context.Context) error {
	if cold == nil || cold.self != cold {
		return sessionLeaseError("the None prepare has no original continuation")
	}
	if cold.managed == nil {
		return cold.Check(ctx, cold.operation)
	}
	cold.mu.Lock()
	defer cold.mu.Unlock()
	if err := cold.checkOriginV1(ctx); err != nil {
		return err
	}
	managed := cold.managed
	if err := managed.checkDormantV1(ctx); err != nil {
		return err
	}
	p := cold.preparation
	if p == nil || p.publication == nil || p.publication.startup != cold.operation {
		return sessionLeaseError("the managed None bootstrap lost its actual pending admission owner")
	}
	target, err := cold.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if err := applyAdmittedRestartMemoryV1(cold.operation, target); err != nil {
		return err
	}
	if err := checkAdmittedRestartSelectionV1(cold.operation, &cold.source, target); err != nil {
		return err
	}
	if !noneColdRoleV1(target) || target.Limits.Declared != 0 {
		return sessionLeaseError("managed None cannot adopt a declared sandbox quantum")
	}
	if err := managed.checkTargetResidualV1(ctx, target); err != nil {
		return err
	}
	argv := []string{managed.origin.executable, "supervise", cold.operation.Primary()}
	if err := managed.manager.rewriteCustodyRuntimeV1(ctx, managed.runtime, target, managed.origin.executable, argv); err != nil {
		return err
	}
	launch := &startupLaunchV1{buildingDone: make(chan struct{}), operation: cold.operation, manifest: target, executable: managed.origin.executableRef, argv: argv, manager: managed.manager, placement: p.publication}
	managed.launch = launch
	defer close(launch.buildingDone)
	if err := managed.manager.events.armLoadedV1(UnitName(target.ID), managed.path, managed.oldInvocation); err != nil {
		return err
	}
	// The actual ordinary admission lock spans StartUnit, its first native
	// birth, public code/namespace hello and real RSS registration. The main
	// cannot acquire placement or admit a factory before we lend OP below.
	err = p.publication.gateControl(ctx, func(control *placementControl) (failure error) {
		defer func() {
			if failure != nil && launch.started {
				failure = errors.Join(failure, managed.settlePreloanV1())
			}
		}()
		if err := cold.checkOriginV1(ctx); err != nil {
			return err
		}
		if err := checkUserDrainLockedV1(ctx, control); err != nil {
			return err
		}
		if record, ok := control.Active[p.publication.operation.token]; !ok || record != p.publication.operation.record || record.Kind != "resume_prepare" {
			return sessionLeaseError("the original ordinary pending admission changed")
		}
		if err := errors.Join(managed.checkPinnedV1(ctx), managed.runtime.service.check(), managed.runtime.pin.check()); err != nil {
			return err
		}
		if err := checkAdmittedRestartCurrentV1(cold.operation, &cold.source); err != nil {
			return err
		}
		facts, err := observeMemoryRoomLockedV1(ctx, "", 0)
		if err != nil {
			return err
		}
		if facts.Room < 0 {
			return noMemoryRoomV1(facts)
		}
		managed.manager.events.markDispatched()
		launch.started = true
		_ = managed.manager.connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
		call := managed.manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).GoWithContext(managed.manager.ctx, systemdManagerInterface+".StartUnit", 0, make(chan *dbus.Call, 1), UnitName(target.ID), "fail")
		_ = managed.manager.connection.SetWriteDeadline(time.Time{})
		call = <-call.Done
		var job dbus.ObjectPath
		if err := call.Store(&job); err != nil {
			switch call.Err.(type) {
			case dbus.Error, *dbus.Error:
				managed.manager.events.recordNegativeReply()
			}
			return err
		}
		if !job.IsValid() || job == "/" {
			return sessionLeaseError("the managed None bootstrap has no exact original native job")
		}
		creation, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		launch.unit, err = managed.manager.finishSupervisorActivationV1(creation, target, managed.origin.executableRef, argv, job, 0)
		cancel()
		if err != nil {
			return err
		}
		fd, err := unix.FcntlInt(uintptr(launch.unit.peer.pidfd), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			return err
		}
		managed.creation = os.NewFile(uintptr(fd), "original-none-bootstrap-pidfd")
		managed.group, err = openNativeStopCgroupV1(target.ID)
		if err != nil {
			return err
		}
		if err := launch.connect(ctx); err != nil {
			return err
		}
		if err := managed.checkRSSV1(ctx); err != nil {
			return err
		}
		facts, err = observeMemoryRoomLockedV1(ctx, "", 0)
		if err != nil {
			return err
		}
		if facts.Room < 0 {
			return noMemoryRoomV1(facts)
		}
		managed.rssRegistered = true
		return nil
	})
	if err != nil {
		return err
	}
	if err := managed.sendPassiveLoanV1(ctx); err != nil {
		return err
	}
	if err := managed.captureRolesV1(ctx); err != nil {
		return err
	}
	return managed.checkPreparedV1(ctx)
}

func (managed *noneManagerCustodyV1) checkRSSV1(ctx context.Context) error {
	if managed == nil || managed.custodyKernelSourceV1 == nil {
		return sessionLeaseError("the managed None RSS owner is absent")
	}
	launch := managed.launch
	if managed.group == nil || managed.creation == nil || launch == nil || launch.unit == nil || launch.nativePeer() == nil || launch.nativePeer().identity == nil || !noneColdRoleV1(launch.manifest) || launch.manifest.Limits.Declared != 0 {
		return sessionLeaseError("the managed None bootstrap lost its actual ordinary native RSS owner")
	}
	if err := launch.unit.Check(ctx); err != nil {
		return err
	}
	if !launch.custodyReleaseSent {
		if err := custodyOnlyPIDV1(managed.group, launch.nativePeer().pid); err != nil {
			return err
		}
	}
	current, err := openNativeStopCgroupV1(managed.operation.Primary())
	if err != nil {
		return err
	}
	held, heldErr := managed.group.Stat()
	named, namedErr := current.Stat()
	closeErr := current.Close()
	if heldErr != nil || namedErr != nil || closeErr != nil || !os.SameFile(held, named) {
		return errors.Join(sessionLeaseError("the original None RSS cgroup was replaced"), heldErr, namedErr, closeErr)
	}
	if _, err := readNativeMemoryValueV1(int(managed.group.Fd()), "memory.current"); err != nil {
		return err
	}
	return errors.Join(launch.unit.Check(ctx), ctx.Err())
}

func (managed *noneManagerCustodyV1) sendPassiveLoanV1(ctx context.Context) (failure error) {
	launch := managed.launch
	if !managed.rssRegistered || launch == nil || launch.loanSent || launch.line == nil {
		return sessionLeaseError("the None bootstrap has no registered original ordinary RSS admission")
	}
	current, err := managed.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if err := checkAdmittedRestartCurrentV1(managed.operation, current); err != nil {
		return err
	}
	runtime, err := startupRuntimeBytes(current)
	if err != nil {
		return err
	}
	loan := startupLoanV1{Version: 1, Primary: managed.operation.Primary(), Nonce: launch.hello.StartupNonce, Purpose: "manager-custody", Need: startupDigest(current.VMNeedV1), Runtime: startupDigest(runtime)}
	raw, err := json.Marshal(loan)
	if err != nil {
		return err
	}
	file, err := managed.operation.native.duplicateLockedFile(ctx)
	if err != nil {
		return err
	}
	defer func() { failure = errors.Join(failure, file.Close()) }()
	if err := launch.placement.gate(ctx, func() error {
		if err := managed.checkRSSV1(ctx); err != nil {
			return err
		}
		launch.loanSent = true
		return launch.line.writeLoan(proto.Request{ID: 1, Op: startupLoanOperationV1, Args: raw}, file)
	}); err != nil {
		return err
	}
	result, err := launch.response(ctx, 1, true)
	if err != nil {
		return err
	}
	var reply struct {
		Nonce string `json:"startup_nonce"`
	}
	if !api.ExactJSONFields(result, reply) || json.Unmarshal(result, &reply) != nil || reply.Nonce != loan.Nonce {
		return sessionLeaseError("the original None bootstrap acknowledged another passive loan")
	}
	return launch.check(ctx)
}
