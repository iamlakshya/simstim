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
	outer := c.gvisorOuter
	var nested *gvisorNestedCreationV1
	if outer != nil {
		nested = outer.nested
	}
	c.mu.Unlock()
	if outer != nil {
		if err := outer.joinActualA(nested); err != nil {
			return c.uncertainWait(err)
		}
	}
	// Keep the original creation descriptors until the purpose-private mode
	// owner confirms whole-source physical closure and settles its fence.
	// This runs before normal Wait is consumed, so uncertainty can be retried.
	if err := c.settleVMMNoFutureBeforeWaitV1(); err != nil {
		return c.uncertainWait(err)
	}
	// Retain all original creation/code handles until the actual all-process
	// cgroup is empty. A logical wait or namespace PID1 exit cannot replace it.
	if err := c.closeGvisorForkFreezerV1(); err != nil {
		return c.uncertainWait(err)
	}
	if outer != nil && outer.fork != nil {
		// A live fork reader checks this Cmd. Join it before taking Cmd.mu.
		if err := outer.fork.close(); err != nil {
			return c.uncertainWait(err)
		}
	}
	c.mu.Lock()
	werr := c.cmd.Wait()
	c.reaped, c.physical = true, true
	if c.pidfd >= 0 {
		c.lifecycleErr = errors.Join(c.lifecycleErr, unix.Close(c.pidfd))
		c.pidfd = -1
	}
	if c.gvisorOuter != nil && c.gvisorOuter.child != nil {
		c.lifecycleErr = errors.Join(c.lifecycleErr, c.gvisorOuter.child.Close())
		c.gvisorOuter.child, c.gvisorOuter.childFD = nil, -1
	}
	if c.gvisorOuter != nil {
		if c.gvisorOuter.nested != nil {
			c.lifecycleErr = errors.Join(c.lifecycleErr, c.gvisorOuter.nested.closeJoinedV1())
		}
	}
	c.lifecycleErr = errors.Join(c.lifecycleErr, c.closePublicationCaptureV1())
	cleanupErr := c.lifecycleErr
	c.mu.Unlock()
	// A controller witness reader holds its own mutex while checking this
	// command. Retire the public code/guest owner AFTER releasing Cmd.mu, once
	// physical is already published, so cleanup never inverts that lock order.
	if outer != nil {
		codeErr := outer.closeCodeV1()
		c.mu.Lock()
		c.lifecycleErr = errors.Join(c.lifecycleErr, codeErr)
		cleanupErr = c.lifecycleErr
		c.mu.Unlock()
	}
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
		return c.closeGvisorForkFreezerV1() == nil
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
	if c.gvisorOuter != nil && !c.gvisorOuter.noChild {
		if c.gvisorOuter.childFD < 0 {
			c.mu.Unlock()
			return false
		}
		outerReady, outerErr := ownedPIDFDReady(c.gvisorOuter.childFD, 0)
		if outerErr != nil {
			c.lifecycleErr = errors.Join(c.lifecycleErr, outerErr)
		}
		if outerErr != nil || !outerReady {
			c.mu.Unlock()
			return false
		}
		if c.gvisorOuter.nested != nil {
			if err := c.gvisorOuter.nested.readyV1(); err != nil {
				c.lifecycleErr = errors.Join(c.lifecycleErr, err)
				c.mu.Unlock()
				return false
			}
		}
	}
	c.phase = phaseWaiting
	c.mu.Unlock()
	// A failed wait cannot establish control of a status-pipe holder that
	// escaped the owned process. Close our metadata end rather than wait on
	// its EOF; actual POLLIN already proves the owned process's closure.
	_ = c.completeOwnedWait(false)
	c.mu.Lock()
	physical := c.physical
	c.mu.Unlock()
	return physical && c.closeGvisorForkFreezerV1() == nil
}

// finish publishes the result before releasing pipes and notifying waiters.
func (c *Cmd) finish(exit *ExitError, release func()) {
	c.mu.Lock()
	c.exit = exit
	c.mu.Unlock()
	release()
	c.doneOnce.Do(func() { close(c.done) })
}
