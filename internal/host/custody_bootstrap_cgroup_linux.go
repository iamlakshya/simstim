//go:build linux

package host

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// The common facts check grants no source or execution. Callers below bind
// the genuine current bootstrap before allowing the SAME cgroup exception.
func custodyBootstrapCgroupFactsV1(oldPIDFD, oldGroup, newGroup *os.File, pid int) error {
	if err := checkCustodyExitedMainV1(oldPIDFD); err != nil {
		return err
	}
	empty, err := originalStopCgroupEmptyV1(oldGroup)
	if err != nil {
		return err
	}
	if empty {
		return nil
	}
	if newGroup == nil || pid <= 0 {
		return sessionLeaseError("a nonempty old cgroup has no actual current bootstrap owner")
	}
	oldInfo, oldErr := oldGroup.Stat()
	newInfo, newErr := newGroup.Stat()
	if oldErr != nil || newErr != nil || !os.SameFile(oldInfo, newInfo) {
		return errors.Join(sessionLeaseError("the retained old cgroup differs from the actual new bootstrap cgroup"), oldErr, newErr)
	}
	return custodyOnlyPIDV1(oldGroup, pid)
}

func (source *custodyKernelSourceV1) checkPassiveOldClosureV1(ctx context.Context) error {
	launch := source.launch
	if launch == nil || launch.custodyReleaseSent || launch.ready || launch.nativePeer() == nil || launch.nativePeer().identity == nil || source.group == nil || source.creation == nil {
		return sessionLeaseError("cgroup reuse has no original bound passive bootstrap")
	}
	if err := launch.check(ctx); err != nil {
		return err
	}
	if err := custodyOnlyPIDV1(source.group, launch.nativePeer().pid); err != nil {
		return err
	}
	if err := custodyBootstrapCgroupFactsV1(source.roles["custody-pidfd-v1"], source.roles["custody-cgroup-v1"], source.group, launch.nativePeer().pid); err != nil {
		return err
	}
	return errors.Join(launch.check(ctx), ctx.Err())
}

func (inherited *custodyInheritanceV1) checkPassiveOldClosureV1(ctx context.Context) error {
	if inherited == nil || inherited.released.Load() || inherited.closed.Load() || inherited.native == nil || inherited.bootstrapPIDFD == nil || inherited.manager == nil || inherited.activation.MainPID != uint32(os.Getpid()) {
		return sessionLeaseError("the inherited cgroup reuse has no actual passive current main")
	}
	current, err := inherited.manager.activation(ctx, inherited.path, UnitName(inherited.native.id))
	if err != nil || !sameUnitActivationProperties(current, inherited.activation) || current.MainPID != uint32(os.Getpid()) {
		return errors.Join(sessionLeaseError("the actual new bootstrap invocation changed before cgroup reuse"), err)
	}
	poll := []unix.PollFd{{Fd: int32(inherited.bootstrapPIDFD.Fd()), Events: unix.POLLIN}}
	n, err := unix.Poll(poll, 0)
	if err != nil || n != 0 {
		return errors.Join(sessionLeaseError("the current main lost its own original native process handle"), err)
	}
	if err := unix.PidfdSendSignal(int(inherited.bootstrapPIDFD.Fd()), 0, nil, 0); err != nil {
		return errors.Join(sessionLeaseError("the passive main has no recognised original own pidfd"), err)
	}
	identity, err := currentNativeHelperIdentityV1()
	if err != nil {
		return err
	}
	executable, err := identity.executable.Stat()
	if err != nil {
		return errors.Join(err, identity.Close())
	}
	if err := identity.Check(executable); err != nil {
		return errors.Join(err, identity.Close())
	}
	if err := identity.Close(); err != nil {
		return err
	}
	group, err := openNativeStopCgroupV1(inherited.native.id)
	if err != nil {
		return err
	}
	failure := custodyOnlyPIDV1(group, os.Getpid())
	if failure == nil {
		failure = custodyBootstrapCgroupFactsV1(inherited.pidfd, inherited.cgroup, group, os.Getpid())
	}
	return errors.Join(failure, group.Close(), inherited.manager.checkNativeManager(), ctx.Err())
}
