//go:build linux

package jail

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// prepareGvisorForkFreezerV1 is called only by the actual original outer
// creator before its SAME exec.Cmd.Start. No caller can supply a cgroup path,
// recover a saved process number or construct this native owner from a row.
func (c *Cmd) prepareGvisorForkFreezerV1() error {
	c.mu.Lock()
	valid := c.gvisorOuter != nil && c.phase == phaseSetup && c.gvisorFreezer == nil && !c.owned && !c.physical
	c.mu.Unlock()
	if !valid || c.ctx == nil || c.cmd == nil || c.cmd.SysProcAttr == nil {
		return gvisorFreezeErrorV1("the gvisor freezer has no original pending creator", nil)
	}
	freezer, err := prepareGvisorFreezerV1(c.ctx)
	c.mu.Lock()
	c.gvisorFreezer = freezer
	if err != nil {
		c.gvisorFreezerOff = err.Error()
	}
	c.mu.Unlock()
	if err != nil && freezer != nil {
		return gvisorFreezeErrorV1("retain the exact gvisor cgroup until its cleanup is proved", err)
	}
	if freezer == nil {
		// Missing delegation disables only fork freezing. It never fabricates
		// support from a wall name or makes an ordinary runtime startup unsafe.
		return nil
	}
	freezer.mu.Lock()
	defer freezer.mu.Unlock()
	native, ok := freezer.control.(*gvisorCgroupV1)
	if !ok || native == nil {
		return gvisorFreezeErrorV1("the gvisor freezer lost its original cgroup creator", nil)
	}
	if err := native.check(false); err != nil {
		return err
	}
	c.cmd.SysProcAttr.UseCgroupFD = true
	c.cmd.SysProcAttr.CgroupFD = int(native.child.Fd())
	return nil
}

func (c *Cmd) bindGvisorForkCreatorV1(pidfd, pid int) error {
	c.mu.Lock()
	freezer := c.gvisorFreezer
	c.mu.Unlock()
	if freezer == nil {
		return nil
	}
	if pidfd < 0 || pid <= 0 {
		return gvisorFreezeErrorV1("the actual gvisor creator has no atomic creation handle", nil)
	}
	fd, err := unix.FcntlInt(uintptr(pidfd), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return gvisorFreezeErrorV1("cannot retain the same gvisor creation pidfd", err)
	}
	freezer.mu.Lock()
	defer freezer.mu.Unlock()
	native, ok := freezer.control.(*gvisorCgroupV1)
	if !ok || native == nil {
		return errors.Join(gvisorFreezeErrorV1("the original gvisor cgroup creator changed", nil), unix.Close(fd))
	}
	if native.launcher != nil {
		return errors.Join(gvisorFreezeErrorV1("the original gvisor creator was already bound", nil), unix.Close(fd))
	}
	return native.bindCreatedLauncherV1(fd, pid)
}

// FreezeAllProcessesV1 stops the actual original gvisor creator and every
// descendant in its own cgroup, including independent agent process groups.
// The supervisor remains outside; only a live unexpired lease may renew.
func (c *Cmd) FreezeAllProcessesV1(ctx context.Context, maxMS int) error {
	if c == nil {
		return gvisorFreezeErrorV1("the original gvisor command is absent", nil)
	}
	if err := c.CheckNativeLiveV1(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	freezer, reason := c.gvisorFreezer, c.gvisorFreezerOff
	valid := c.gvisorOuter != nil && c.owned && !c.physical && !c.reaped && !c.groupUnsafe
	c.mu.Unlock()
	if !valid || freezer == nil {
		if reason == "" {
			reason = "the original gvisor command has no delegated all-process cgroup"
		}
		return gvisorFreezeErrorV1(reason, nil)
	}
	return freezer.freeze(ctx, maxMS)
}

// ThawAllProcessesV1 ends only this actual command's owned freeze lease.
// A failed kernel thaw retains the same native owner and its retry timer.
func (c *Cmd) ThawAllProcessesV1() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	freezer := c.gvisorFreezer
	c.mu.Unlock()
	if freezer == nil {
		return nil
	}
	return freezer.releaseLease()
}

// FreezeAvailableV1 reports this actual command's current original native
// cgroup capability. It does not infer support from delegation metadata.
func (c *Cmd) FreezeAvailableV1() bool {
	if c == nil || c.CheckNativeLiveV1(c.ctx) != nil {
		return false
	}
	c.mu.Lock()
	freezer, gvisor := c.gvisorFreezer, c.gvisorOuter != nil
	c.mu.Unlock()
	return gvisor && freezer.available()
}

// FreezeReasonV1 reports why this original gvisor command lacks a freezer.
func (c *Cmd) FreezeReasonV1() string {
	if c == nil {
		return "the original gvisor command is absent"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gvisorFreezerOff
}

func (c *Cmd) closeGvisorForkFreezerV1() error {
	c.mu.Lock()
	freezer := c.gvisorFreezer
	c.mu.Unlock()
	if freezer == nil {
		return nil
	}
	if err := freezer.closeEmpty(); err != nil {
		return fmt.Errorf("retain the original gvisor cgroup until its kernel population is empty: %w", err)
	}
	return nil
}
