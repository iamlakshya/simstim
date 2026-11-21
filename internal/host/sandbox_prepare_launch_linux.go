//go:build linux

package host

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/proto"
)

// The new purpose uses the existing original native OP sender, including its
// placement, current memory, original code and retained descriptor checks.
func (launch *startupLaunchV1) sendSandboxPrepareLoanV1(ctx context.Context) error {
	if launch == nil || launch.operation == nil || !launch.operation.IsSandboxPrepareV1() {
		return sessionLeaseError("sandbox preparation lost its original fixed-purpose operation")
	}
	return launch.sendOperationLoan(ctx)
}

func applySandboxPrepareChildAffinityV1(launch *startupLaunchV1) error {
	if launch == nil || launch.operation == nil || !launch.operation.IsSandboxPrepareV1() ||
		launch.command == nil || launch.command.Process == nil || launch.command.ProcessState != nil ||
		launch.child == nil || launch.child.pid != launch.command.Process.Pid {
		return sessionLeaseError("the fixed CPU boundary has no original unreaped creation")
	}
	if err := launch.child.checkLive(); err != nil {
		return err
	}
	// This numeric argument remains pinned by the SAME unreaped Process and
	// its original creation pidfd. It is never recovered from metadata.
	var allowed unix.CPUSet
	if err := unix.SchedGetaffinity(launch.child.pid, &allowed); err != nil {
		return err
	}
	var single unix.CPUSet
	for cpu := 0; cpu < 1024; cpu++ {
		if allowed.IsSet(cpu) {
			single.Set(cpu)
			break
		}
	}
	if single.Count() != 1 {
		return sessionLeaseError("the original helper has no usable single CPU affinity")
	}
	if err := unix.SchedSetaffinity(launch.child.pid, &single); err != nil {
		return err
	}
	var observed unix.CPUSet
	if err := unix.SchedGetaffinity(launch.child.pid, &observed); err != nil {
		return err
	}
	if observed != single {
		return sessionLeaseError("the original helper did not retain its fixed one CPU boundary")
	}
	return launch.child.checkLive()
}

// stopAndJoinSandboxPrepareLaunchV1 uses only this launch's original native
// line and retained creation. After a loan, positive native driver cleanup ACK
// and positive supervisor exit are BOTH required. The caller retains this same
// cleanup stack if either remains unconfirmed; an RPC deadline cannot release it.
func stopAndJoinSandboxPrepareLaunchV1(ctx context.Context, launch *startupLaunchV1, id string, m *Manifest) error {
	if launch == nil {
		// launchSupervisorOperation returns its creation owner on EVERY path
		// after creation admission. No launch therefore means no native start.
		return nil
	}
	if launch.operation == nil || !launch.operation.IsSandboxPrepareV1() ||
		launch.operation.Primary() != id || m == nil || m.ID != id || launch.manifest == nil || launch.manifest.ID != id {
		return sessionLeaseError("preparation cleanup lost its original operation and native creation")
	}
	if launch.buildingDone != nil {
		<-launch.buildingDone
	}
	if !launch.loanSent {
		// Before the loan the authenticated supervisor cannot construct a
		// runtime. Close joins its SAME original child/manager creation,
		// including an ambiguous first manager dispatch.
		if err := launch.Close(); err != nil {
			return err
		}
		if launch.started && !launch.exited {
			return sessionLeaseError("the original unloaned helper creation is unresolved")
		}
		launch.cleanupConfirmed = true
		return nil
	}
	if !launch.cleanupConfirmed {
		if err := launch.cancelSandboxPrepareV1(ctx); err != nil {
			return err
		}
	}
	peer := launch.nativePeer()
	if peer == nil || peer.pidfd < 0 {
		return sessionLeaseError("the loaned preparation has no original native exit handle")
	}
	for {
		polls := []unix.PollFd{{Fd: int32(peer.pidfd), Events: unix.POLLIN}}
		n, err := unix.Poll(polls, 100)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if n == 1 && polls[0].Revents&unix.POLLIN != 0 {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if launch.command != nil {
		_ = launch.waitOriginalCommand() // exit status is separate from native cleanup
		if launch.command.ProcessState == nil {
			return sessionLeaseError("the original preparation helper is not reaped")
		}
	}
	if launch.unit != nil {
		if launch.manager == nil {
			return sessionLeaseError("the preparation unit lost its original native manager")
		}
		if err := launch.waitSandboxPrepareNativeVacancyV1(ctx); err != nil {
			return err
		}
	}
	launch.mu.Lock()
	launch.exited, launch.ready = true, false
	launch.mu.Unlock()
	return launch.Close()
}

func (launch *startupLaunchV1) cancelSandboxPrepareV1(ctx context.Context) error {
	if launch.operation == nil || !launch.operation.IsSandboxPrepareV1() || launch.line == nil {
		return sessionLeaseError("preparation cleanup lost its original private control line")
	}
	if launch.readFailed || launch.prepareFailed || launch.line.preparePoison {
		// Only a read deadline is resumable. EOF, malformed native data or
		// another sender/response cannot be replaced by a new reader.
		_ = launch.line.Close()
		return sessionLeaseError("the original preparation cleanup acknowledgement is unconfirmed")
	}
	_ = launch.line.SetDeadline(time.Time{})
	if !launch.prepareCancelSent {
		launch.prepareCancelSent = true
		if err := launch.line.WriteFrame(proto.TRequest, proto.Request{ID: 4, Op: cancelPendingStartOp}); err != nil {
			launch.prepareFailed = true
			_ = launch.line.Close()
			return err
		}
	}
	if err := launch.drainSandboxPrepareRepliesV1(ctx); err != nil {
		return err
	}
	raw, err := launch.response(ctx, 4, false)
	if err != nil {
		return err
	}
	diagnostic, err := decodeSandboxPrepareCleanupDiagnosticV1(raw)
	if err != nil {
		launch.prepareFailed = true
		return err
	}
	launch.prepareCleanupDiagnostic = diagnostic
	launch.cleanupConfirmed = true
	return nil
}

// The SAME original transient creation must be physically gone before its
// outside OP can retire. Main exit and ActiveState inactive/failed do not
// establish whole-unit closure. Never StopUnit a possible replacement.
func (launch *startupLaunchV1) waitSandboxPrepareNativeVacancyV1(ctx context.Context) error {
	if ctx == nil || launch == nil || launch.operation == nil || !launch.operation.IsSandboxPrepareV1() ||
		launch.operation.startup != launch || launch.unit == nil || launch.manager == nil ||
		launch.unit.manager != launch.manager || launch.unit.unit != UnitName(launch.operation.Primary()) {
		return sessionLeaseError("prepare vacancy has no same original native activation and operation")
	}
	peer := launch.nativePeer()
	if peer == nil || peer != launch.unit.peer || peer.pidfd < 0 {
		return sessionLeaseError("prepare vacancy lost its original native creation pidfd")
	}
	for {
		if err := errors.Join(ctx.Err(), launch.manager.checkNativeManager()); err != nil {
			return err
		}
		if !launch.operation.Valid(launch.operation.Primary()) {
			return sessionLeaseError("prepare vacancy lost its original owning operation")
		}
		polls := []unix.PollFd{{Fd: int32(peer.pidfd), Events: unix.POLLIN}}
		n, err := unix.Poll(polls, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || n != 1 || polls[0].Revents&unix.POLLIN == 0 || polls[0].Revents&(unix.POLLERR|unix.POLLNVAL|unix.POLLOUT) != 0 {
			return errors.Join(sessionLeaseError("prepare vacancy has no positive original native process exit"), err)
		}
		if err := launch.manager.requireAuxiliaryUnitAbsentV1(ctx, launch.unit.unit); err == nil {
			// The original loan's final native admission already required this
			// exact canonical cgroup2 unit. This is a kernel read, never a
			// saved unit name, status, counter or manager-only observation.
			group, err := readMemoryUnitV1(ctx, launch.operation.Primary(), true)
			if err != nil {
				return err
			}
			if !group.exists {
				if err := launch.manager.requireAuxiliaryUnitAbsentV1(ctx, launch.unit.unit); err == nil {
					if !launch.operation.Valid(launch.operation.Primary()) {
						return sessionLeaseError("prepare vacancy lost its original operation during its native recheck")
					}
					return errors.Join(launch.manager.checkNativeManager(), ctx.Err())
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
