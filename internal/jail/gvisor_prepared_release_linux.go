package jail

import "errors"

// ReleasePreparedV1 retires this actual gvisor constructor's retained native
// code and guest descriptions. It creates no stopped/source permission. A new
// command is fenced under the same Start mutex; a started command needs its
// original positive physical join before any preparation can be retired.
func (c *Cmd) ReleasePreparedV1() error {
	if c == nil {
		return gvisorHandoffFailureV2("the actual gvisor preparation owner is missing")
	}
	c.mu.Lock()
	outer := c.gvisorOuter
	if outer == nil {
		c.mu.Unlock()
		return gvisorHandoffFailureV2("this command has no gvisor preparation owner")
	}
	if c.phase == phaseNew {
		if c.owned || c.cmd == nil || c.cmd.Process != nil {
			c.mu.Unlock()
			return gvisorHandoffFailureV2("the unstarted gvisor creation is ambiguous")
		}
		c.phase, c.physical = phaseFailed, true
		c.mu.Unlock()
		c.closeEnds(true)
		c.closePins()
	} else {
		c.mu.Unlock()
		if !c.PhysicalExitV1() {
			return gvisorHandoffFailureV2("the live or uncertain original gvisor creator still owns preparation")
		}
	}
	if err := outer.closeCodeV1(); err != nil {
		c.mu.Lock()
		c.lifecycleErr = errors.Join(c.lifecycleErr, err)
		c.mu.Unlock()
		return err
	}
	return nil
}
