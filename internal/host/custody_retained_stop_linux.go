//go:build linux

package host

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

// retireNativeRuntimeAfterExitV1 consumes only a genuine original invocation's
// captured cleanup handles. It does not widen the ordinary Stopped predicate.
// Physical exit and the SAME original cgroup's emptiness precede every unlink.
func (owner *StoppedSessionV1) retireNativeRuntimeAfterExitV1(ctx context.Context) error {
	if owner == nil || owner.invocation == nil {
		return auxiliaryUnavailableV1("retained stop lost its actual native invocation")
	}
	invocation := owner.invocation
	if invocation.runtime == nil {
		return nil
	}
	if err := errors.Join(owner.manager.checkNativeManager(), owner.operation.native.check(ctx)); err != nil {
		return err
	}
	polls := []unix.PollFd{{Fd: int32(invocation.peer.pidfd), Events: unix.POLLIN}}
	n, err := unix.Poll(polls, 0)
	if err != nil || n != 1 || polls[0].Revents&unix.POLLIN == 0 {
		return errors.Join(auxiliaryUnavailableV1("runtime retirement has no positive original main exit"), err)
	}
	empty, err := originalStopCgroupEmptyV1(invocation.cgroup)
	if err != nil || !empty {
		return errors.Join(auxiliaryUnavailableV1("runtime retirement still has original native writers"), err)
	}
	if invocation.runtime.retired {
		return nil
	}
	entries, err := owner.manager.dumpCustodyStoreV1(ctx, invocation.unit)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return auxiliaryUnavailableV1("the retained-stop class cannot clear an actual manager custody bundle")
	}
	return invocation.runtime.retireConfigV1(ctx, owner.manager)
}
