//go:build linux

package host

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

func (owner *StoppedSessionV1) settleNativeStopV1() error {
	if owner == nil || !owner.stopSent || owner.invocation == nil || owner.manager == nil || owner.operation == nil {
		return auxiliaryUnavailableV1("physical stop has no actual original native source")
	}
	if owner.settled {
		return nil
	}
	if err := owner.manager.checkNativeManager(); err != nil {
		return owner.retainUnknownStopV1(err)
	}
	// Signal only the previously verified same invocation. The descriptor below
	// remains the actual whole cgroup even if its name later disappears.
	if owner.invocation.peer.checkLive() == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := owner.invocation.Check(ctx)
		if err == nil {
			var job dbus.ObjectPath
			err = owner.manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".StopUnit", 0, owner.invocation.unit, "fail").Store(&job)
		}
		cancel()
		if err != nil && owner.invocation.peer.checkLive() == nil {
			return owner.retainUnknownStopV1(err)
		}
	}
	grace := time.Now().Add(30 * time.Second)
	killed := false
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := owner.checkStoppedNativeFactsV1(ctx)
		cancel()
		if err == nil {
			break
		}
		if owner.manager.checkNativeManager() != nil {
			return owner.retainUnknownStopV1(err)
		}
		if !killed && !time.Now().Before(grace) {
			empty, checkErr := originalStopCgroupEmptyV1(owner.invocation.cgroup)
			if checkErr != nil {
				return owner.retainUnknownStopV1(checkErr)
			}
			if !empty {
				if err := killOriginalStopCgroupV1(owner.invocation.cgroup); err != nil {
					return owner.retainUnknownStopV1(err)
				}
			}
			killed = true
		}
		time.Sleep(25 * time.Millisecond)
	}
	if owner.holder != nil {
		if err := owner.holder.Close(); err != nil {
			return owner.retainUnknownStopV1(err)
		}
		owner.holder = nil
	}
	holder, err := openOwnedSessionFileLease(context.Background(), owner.operation.Primary(), holderLockFile, 0)
	if err != nil {
		return owner.retainUnknownStopV1(err)
	}
	if err := owner.operation.native.rootRef.CheckRoot(holder.root); err != nil {
		_ = holder.Close()
		return owner.retainUnknownStopV1(err)
	}
	owner.holder = holder
	owner.settled = true
	return nil
}
func (owner *StoppedSessionV1) checkStoppedNativeFactsV1(ctx context.Context) error {
	if owner == nil || owner.invocation == nil || owner.invocation.peer == nil || owner.manager == nil {
		return auxiliaryUnavailableV1("whole-stop lost its original native objects")
	}
	if err := errors.Join(owner.manager.checkNativeManager(), ctx.Err()); err != nil {
		return err
	}
	polls := []unix.PollFd{{Fd: int32(owner.invocation.peer.pidfd), Events: unix.POLLIN}}
	count, err := unix.Poll(polls, 0)
	if err != nil || count != 1 || polls[0].Revents&unix.POLLIN == 0 {
		return errors.Join(auxiliaryUnavailableV1("the original supervisor has no positive native exit"), err)
	}
	empty, err := originalStopCgroupEmptyV1(owner.invocation.cgroup)
	if err != nil || !empty {
		return errors.Join(auxiliaryUnavailableV1("the original native unit still has processes or uncertain membership"), err)
	}
	auxiliary, err := AuxUnitNameV1(owner.operation.Primary())
	if err != nil {
		return err
	}
	if err := owner.manager.requireAuxiliaryUnitAbsentV1(ctx, owner.invocation.unit); err != nil {
		return err
	}
	if err := owner.manager.requireAuxiliaryUnitAbsentV1(ctx, auxiliary); err != nil {
		return err
	}
	primary, err := readMemoryUnitV1(ctx, owner.operation.Primary(), true)
	if err != nil {
		return err
	}
	aux, err := readAuxMemoryUnitV1(ctx, owner.operation.Primary())
	if err != nil {
		return err
	}
	if primary.exists || aux.exists {
		return auxiliaryUnavailableV1("a canonical ordinary or auxiliary cgroup still exists")
	}
	return errors.Join(owner.manager.requireAuxiliaryUnitAbsentV1(ctx, owner.invocation.unit), owner.manager.requireAuxiliaryUnitAbsentV1(ctx, auxiliary), owner.manager.checkNativeManager(), ctx.Err())
}
func originalStopCgroupEmptyV1(group *os.File) (bool, error) {
	if group == nil {
		return false, auxiliaryUnavailableV1("the original cgroup descriptor ended")
	}
	var native unix.Stat_t
	var filesystem unix.Statfs_t
	if err := unix.Fstat(int(group.Fd()), &native); err != nil {
		return false, err
	}
	if err := unix.Fstatfs(int(group.Fd()), &filesystem); err != nil || filesystem.Type != unix.CGROUP2_SUPER_MAGIC {
		return false, errors.Join(auxiliaryUnavailableV1("the original native group is not cgroup v2"), err)
	}
	// The kernel cannot remove a populated cgroup tree. This is a fact from
	// the SAME retained native directory, never a guessed replacement path.
	if native.Nlink == 0 {
		return true, nil
	}
	descriptor, err := unix.Openat2(int(group.Fd()), "cgroup.events", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return false, err
	}
	file := os.NewFile(uintptr(descriptor), "original-stop-cgroup-events")
	raw, readErr := io.ReadAll(io.LimitReader(file, 257))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(raw) > 256 {
		return false, errors.Join(readErr, closeErr, auxiliaryUnavailableV1("the original cgroup population is unavailable or oversized"))
	}
	seen := map[string]bool{}
	populated := ""
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		key, value, ok := strings.Cut(line, " ")
		if !ok || seen[key] || key != "populated" && key != "frozen" || value != "0" && value != "1" {
			return false, auxiliaryUnavailableV1("the original cgroup population has an unknown native shape")
		}
		seen[key] = true
		if key == "populated" {
			populated = value
		}
	}
	if populated == "" {
		return false, auxiliaryUnavailableV1("the original cgroup has no native population value")
	}
	return populated == "0", nil
}
func killOriginalStopCgroupV1(group *os.File) error {
	descriptor, err := unix.Openat2(int(group.Fd()), "cgroup.kill", &unix.OpenHow{Flags: unix.O_WRONLY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(descriptor), "original-stop-cgroup-kill")
	count, err := file.Write([]byte("1\n"))
	closeErr := file.Close()
	if count != 2 && err == nil {
		err = io.ErrShortWrite
	}
	return errors.Join(err, closeErr)
}
func (owner *StoppedSessionV1) retainUnknownStopV1(failure error) error {
	// An unknown original manager/process cannot be renewed from metadata.
	// Retain the actual controller stack and OP instead of releasing on a timeout.
	_ = failure
	select {}
}
