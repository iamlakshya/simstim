//go:build linux

package jail

// JoinPhysicalV1 waits on the SAME creation pidfd after a logical wait error.
// It grants no numeric PID or process-group authority. Callers must retain the
// command and its resources if the native observation still fails.
func (c *Cmd) JoinPhysicalV1() error {
	c.mu.Lock()
	if c.physical {
		err := c.lifecycleErr
		c.mu.Unlock()
		return err
	}
	if c.phase != phaseUncertain || !c.owned || c.pidfd < 0 {
		c.mu.Unlock()
		return failure("the jail has no uncertain original native wait to join", "Retain the original command until its actual wait completes.")
	}
	fd := c.pidfd
	c.phase = phaseWaiting
	c.mu.Unlock()
	if err := waitOwnedPIDFD(fd, -1); err != nil {
		c.mu.Lock()
		c.phase, c.groupUnsafe = phaseUncertain, true
		c.mu.Unlock()
		return stepFailed("the original jail pidfd still has no physical exit evidence", "Retain this same command and retry its native join.", err)
	}
	return c.completeOwnedWait(false)
}
