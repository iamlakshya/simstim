//go:build linux

package host

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func (source *custodyKernelSourceV1) captureDormantGroupV1(ctx context.Context) error {
	group, err := openNativeStopCgroupV1(source.operation.Primary())
	if errors.Is(err, unix.ENOENT) {
		return source.manager.checkNativeManager()
	}
	if err != nil {
		return err
	}
	source.dormantGroup = group // Own the actual object before checking failures.
	empty, err := originalStopCgroupEmptyV1(group)
	if err != nil || !empty {
		return errors.Join(sessionLeaseError("the retained service still has original cgroup writers"), err)
	}
	return errors.Join(source.checkPinnedV1(ctx), ctx.Err())
}

// Empty PIDs do not authorise lowering a cgroup's execution limit below its
// actual retained memory. No DATA or phase flag replaces these native reads.
func (source *custodyKernelSourceV1) checkTargetResidualV1(ctx context.Context, target *Manifest) error {
	if source == nil || target == nil || target.ID != source.operation.Primary() || target.Limits.MemoryMax <= 0 {
		return sessionLeaseError("the retained original cgroup has no exact selected execution limit")
	}
	current, err := openNativeStopCgroupV1(source.operation.Primary())
	if source.dormantGroup == nil {
		if current != nil {
			_ = current.Close()
			return sessionLeaseError("a native cgroup appeared after original dormant capture")
		}
		if !errors.Is(err, unix.ENOENT) {
			return err
		}
		return errors.Join(source.checkPinnedV1(ctx), ctx.Err())
	}
	var held unix.Stat_t
	if statErr := unix.Fstat(int(source.dormantGroup.Fd()), &held); statErr != nil {
		if current != nil {
			_ = current.Close()
		}
		return statErr
	}
	if held.Nlink == 0 {
		if current != nil {
			_ = current.Close()
			return sessionLeaseError("the retired original cgroup name was replaced")
		}
		if !errors.Is(err, unix.ENOENT) {
			return err
		}
		return errors.Join(source.checkPinnedV1(ctx), ctx.Err())
	}
	if err != nil {
		return err
	}
	oldInfo, oldErr := source.dormantGroup.Stat()
	newInfo, newErr := current.Stat()
	closeErr := current.Close()
	if oldErr != nil || newErr != nil || closeErr != nil || !os.SameFile(oldInfo, newInfo) {
		return errors.Join(sessionLeaseError("the original retained cgroup was replaced before cap change"), oldErr, newErr, closeErr)
	}
	empty, err := originalStopCgroupEmptyV1(source.dormantGroup)
	if err != nil || !empty {
		return errors.Join(sessionLeaseError("the retained original cgroup regained writers before cap change"), err)
	}
	used, err := readNativeMemoryValueV1(int(source.dormantGroup.Fd()), "memory.current")
	if err != nil {
		return err
	}
	swap, err := readNativeMemoryValueV1(int(source.dormantGroup.Fd()), "memory.swap.current")
	if err != nil {
		return err
	}
	if used > target.Limits.MemoryMax || swap != 0 {
		return sessionLeaseError("the original retained cgroup resources do not fit the selected lower execution limit")
	}
	return errors.Join(source.checkPinnedV1(ctx), ctx.Err())
}
