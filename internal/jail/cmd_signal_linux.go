//go:build linux

package jail

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// watch kills the whole jail when the context ends first.
func (c *Cmd) watch() {
	select {
	case <-c.ctx.Done():
		c.killGroup()
	case <-c.done:
	}
}

// killGroup targets the original group while the unreaped owned child pins
// its number. A failed wait revokes group-number signalling permanently;
// subsequent cleanup uses only the exact creation pidfd. This makes no claim
// about a descendant that escaped that group or its namespace.
func (c *Cmd) killGroup() error {
	readonlyErr := c.killReadonlyInitV1(unix.SIGKILL)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.reaped && c.owned {
		err := c.signalOwnedLocked(unix.SIGKILL)
		if err != nil {
			c.lifecycleErr = errors.Join(c.lifecycleErr, readonlyErr, err)
			return errors.Join(readonlyErr, err)
		}
	}
	c.lifecycleErr = errors.Join(c.lifecycleErr, readonlyErr)
	return readonlyErr
}

// signalOwnedLocked checks current child ownership through the creation
// descriptor before any group-number signal. This also fences a concurrent
// watcher against a wait that just lost ownership. Caller holds mu.
func (c *Cmd) signalOwnedLocked(sig syscall.Signal) error {
	var checkErr error
	if sig == unix.SIGKILL && !c.groupUnsafe {
		var info unix.Siginfo
		for {
			checkErr = ownedWaitid(unix.P_PIDFD, c.pidfd, &info, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, nil)
			if errors.Is(checkErr, unix.EINTR) {
				continue
			}
			break
		}
		if checkErr != nil {
			c.groupUnsafe = true
		}
	}
	var signalErr error
	if sig == unix.SIGKILL && !c.groupUnsafe {
		signalErr = ownedGroupSignal(-c.pid, sig)
	} else {
		signalErr = ownedPidfdSignal(c.pidfd, sig, nil, 0)
	}
	if errors.Is(signalErr, unix.ESRCH) {
		signalErr = nil
	}
	var outerErr error
	if c.gvisorOuter != nil && c.gvisorOuter.childFD >= 0 {
		outerErr = ownedPidfdSignal(c.gvisorOuter.childFD, sig, nil, 0)
		if errors.Is(outerErr, unix.ESRCH) {
			outerErr = nil
		}
	}
	var nestedErr error
	if c.gvisorOuter != nil && c.gvisorOuter.nested != nil {
		nestedErr = c.gvisorOuter.nested.signalV1(sig)
	}
	return errors.Join(checkErr, signalErr, outerErr, nestedErr)
}

// Signal sends sig to the jail, whose init passes it on to the command.
// During Start's set-up, it kills the jail instead, and Start fails.
func (c *Cmd) Signal(sig os.Signal) error {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return failure("a jail takes only a syscall.Signal", "Pass a signal such as syscall.SIGTERM.")
	}
	readonlyErr := c.killReadonlyInitV1(s)
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.phase == phaseSetup:
		c.abortOnce.Do(func() { close(c.abort) })
		return nil
	case (c.phase == phaseRunning || c.phase == phaseWaiting || c.phase == phaseUncertain) && !c.reaped:
		err := errors.Join(readonlyErr, c.signalOwnedLocked(s))
		if err != nil {
			c.lifecycleErr = errors.Join(c.lifecycleErr, err)
			return stepFailed("cannot signal the jail", "Check that the jail still runs.", err)
		}
		return nil
	default:
		return failure("the jail is not running", "Signal a jail between a Start that worked and the end of its Wait.")
	}
}
