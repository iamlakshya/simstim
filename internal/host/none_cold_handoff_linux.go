//go:build linux

package host

import (
	"context"
	"golang.org/x/sys/unix"
	"os"
)

func consumeNoneColdDispatchLockedV1(ctx context.Context, operation *SessionOperation, publication *runtimePublication) error {
	if operation == nil || operation.noneCold == nil {
		return nil
	}
	cold := operation.noneCold
	cold.mu.Lock()
	defer cold.mu.Unlock()
	launch := operation.startup
	if cold.closed || cold.handed || !cold.bound || launch == nil || launch.operation != operation || launch.placement != publication || publication == nil || publication.startup != operation || launch.started || launch.loanSent {
		return sessionLeaseError("the None restart has no exact actual native dispatcher")
	}
	if err := cold.checkOriginV1(ctx); err != nil {
		return err
	}
	if err := cold.checkVacancyV1(ctx); err != nil {
		return err
	}
	if !cold.published {
		return sessionLeaseError("the None restart has no actual canonical restart publication")
	}
	if err := cold.holder.Close(); err != nil {
		return err
	}
	cold.holder = nil
	cold.handed = true
	cold.startup = launch
	return ctx.Err()
}

// The managerless dispatcher inherits a duplicate of this SAME exclusive
// holder OFD. It must not reopen a path while the source retains the lease.
func noneColdLaunchHolderV1(ctx context.Context, operation *SessionOperation) (*os.File, error) {
	if operation == nil || operation.noneCold == nil {
		return nil, sessionLeaseError("the None launch has no original continuation")
	}
	cold := operation.noneCold
	cold.mu.Lock()
	defer cold.mu.Unlock()
	if !cold.bound || cold.handed || cold.closed || cold.holder == nil {
		return nil, sessionLeaseError("the original None holder is not available for its new creator")
	}
	if err := cold.checkOriginV1(ctx); err != nil {
		return nil, err
	}
	if err := cold.checkVacancyV1(ctx); err != nil {
		return nil, err
	}
	return duplicateNoneColdHolderV1(ctx, cold)
}

func retainNoneColdCreationV1(operation *SessionOperation, launch *startupLaunchV1) error {
	if operation == nil || operation.noneCold == nil {
		return nil
	}
	cold := operation.noneCold
	cold.mu.Lock()
	defer cold.mu.Unlock()
	if !cold.handed || cold.startup != launch || operation.startup != launch || cold.creation != nil {
		return sessionLeaseError("the None restart has no original new creation")
	}
	peer := launch.nativePeer()
	if peer == nil {
		return sessionLeaseError("the actual None creator has no atomic process handle")
	}
	peer.mu.Lock()
	defer peer.mu.Unlock()
	if peer.closed || peer.pidfd < 0 {
		return sessionLeaseError("the actual None creator ended before its retention")
	}
	fd, err := unix.FcntlInt(uintptr(peer.pidfd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return err
	}
	cold.creation = os.NewFile(uintptr(fd), "actual-new-none-creation")
	return nil
}

func (cold *NoneColdResumeV1) checkTakeoverV1(ctx context.Context) error {
	if cold.managed != nil {
		return cold.managed.checkTakeoverV1(ctx)
	}
	launch := cold.startup
	if !cold.bound || !cold.handed || cold.closed || launch == nil || cold.operation.startup != launch || launch.operation != cold.operation || launch.placement == nil || launch.placement.startup != cold.operation || launch.nativePeer() == nil || launch.nativePeer().identity == nil || launch.line == nil || cold.creation == nil || BootID() != cold.boot {
		return sessionLeaseError("the None continuation has no actual native creation and code/namespace takeover")
	}
	if err := launch.check(ctx); err != nil {
		return err
	}
	if err := cold.checkRetainedCreationV1(); err != nil {
		return err
	}
	current, err := cold.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if !noneColdRoleV1(current) || !noneColdRoleV1(launch.manifest) || current.ID != cold.source.ID {
		return sessionLeaseError("the original None role changed before native takeover")
	}
	return launch.nativePeer().checkLive()
}

func checkNoneColdTakeoverV1(ctx context.Context, operation *SessionOperation) error {
	if operation == nil || operation.noneCold == nil {
		return nil
	}
	cold := operation.noneCold
	cold.mu.Lock()
	defer cold.mu.Unlock()
	if err := cold.checkOriginV1(ctx); err != nil {
		return err
	}
	return cold.checkTakeoverV1(ctx)
}
