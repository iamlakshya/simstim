//go:build linux

package jail

import (
	"errors"
	"golang.org/x/sys/unix"
	"io"
)

// Wait waits for the jail to end, kills any process of it that is left,
// and returns how its command ended: nil, an *ExitError, or ctx's error
// when ctx ended the jail. It waits on no copy of stdio: there is none.
// It closes the caller's ends of the pipes, as exec.Cmd's Wait does.
func (c *Cmd) Wait() error {
	c.mu.Lock()
	switch c.phase {
	case phaseRunning, phaseUncertain:
		c.phase = phaseWaiting
	case phaseWaiting:
		c.mu.Unlock()
		return failure("Wait was already called on this jail", "Call Wait once for each completed Start.")
	case phaseFailed:
		result := c.lifecycleErr
		c.mu.Unlock()
		return errors.Join(failure("the jail did not start", "Read the error that Start returned."), result)
	default:
		c.mu.Unlock()
		return failure("the jail was not started", "Call Start before Wait.")
	}
	fd := c.pidfd
	readStatus := !c.groupUnsafe
	c.mu.Unlock()
	var info unix.Siginfo
	for {
		err := ownedWaitid(unix.P_PIDFD, fd, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return c.uncertainWait(err)
		}
		break
	}
	// The exact creation pidfd identifies an unreaped, exited child. Only
	// this positive wait permits the existing numeric group cleanup. An
	// earlier failed wait permanently revoked that group-number authority.
	_ = c.killGroup()
	if err := waitOwnedPIDFD(fd, -1); err != nil {
		return c.uncertainWait(err)
	}
	return c.completeOwnedWait(readStatus)
}

// uncertainWait retains the actual native child handle and disables numeric
// group signalling. Logical completion, a status message and EOF are not exit.
func (c *Cmd) uncertainWait(err error) error {
	c.mu.Lock()
	c.phase, c.groupUnsafe = phaseUncertain, true
	c.lifecycleErr = errors.Join(c.lifecycleErr, err)
	result := c.lifecycleErr
	c.mu.Unlock()
	return stepFailed("cannot prove the owned jail process has exited", "Retain its runtime resources and retry cleanup through the same command owner.", result)
}

// completeOwnedWait is called only after actual POLLIN on the creation pidfd.
// The mutex fences the reaping boundary against every numeric group signal.
func (c *Cmd) completeOwnedWait(readStatus bool) error {
	c.mu.Lock()
	werr := c.cmd.Wait()
	c.reaped, c.physical = true, true
	if c.pidfd >= 0 {
		c.lifecycleErr = errors.Join(c.lifecycleErr, unix.Close(c.pidfd))
		c.pidfd = -1
	}
	cleanupErr := c.lifecycleErr
	c.mu.Unlock()
	var b []byte
	var readErr, closeErr error
	if c.status != nil {
		if readStatus {
			b, readErr = io.ReadAll(c.status)
		}
		closeErr = c.status.Close()
	}
	exit := parseStatus(string(b))
	if exit == nil && c.cmd.ProcessState != nil {
		exit = exitOf(c.cmd.ProcessState)
	}
	c.finish(exit, func() { c.closeEnds(true) })
	var result error
	switch {
	case exit != nil && exit.Code == 0:
	case c.ctx.Err() != nil && parseStatus(string(b)) == nil:
		result = c.ctx.Err()
	case exit != nil:
		result = exit
	}
	// The actual owned wait and cleanup errors survive a claimed exit status.
	result = errors.Join(result, werr, cleanupErr, readErr, closeErr)
	c.mu.Lock()
	c.lifecycleErr = result
	c.mu.Unlock()
	return result
}

// PhysicalExitV1 proves closure of this owner's actual jail process. It does
// not assert a clean exit or claim closure of descendants that escaped it.
// A failed wait retains the creation pidfd; a later positive nonblocking poll
// can finish that SAME owner's wait without finding or signalling a saved PID.
func (c *Cmd) PhysicalExitV1() bool {
	c.mu.Lock()
	if c.physical {
		c.mu.Unlock()
		return true
	}
	if c.phase != phaseUncertain || !c.owned || c.pidfd < 0 {
		c.mu.Unlock()
		return false
	}
	ready, err := ownedPIDFDReady(c.pidfd, 0)
	if err != nil {
		c.lifecycleErr = errors.Join(c.lifecycleErr, err)
	}
	if err != nil || !ready {
		c.mu.Unlock()
		return false
	}
	c.phase = phaseWaiting
	c.mu.Unlock()
	// A failed wait cannot establish control of a status-pipe holder that
	// escaped the owned process. Close our metadata end rather than wait on
	// its EOF; actual POLLIN already proves the owned process's closure.
	_ = c.completeOwnedWait(false)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.physical
}

// finish publishes the result before releasing pipes and notifying waiters.
func (c *Cmd) finish(exit *ExitError, release func()) {
	c.mu.Lock()
	c.exit = exit
	c.mu.Unlock()
	release()
	c.doneOnce.Do(func() { close(c.done) })
}
