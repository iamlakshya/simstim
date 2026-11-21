package supervisor

import "time"

// Adopt an actual returned object before checking the constructor error. This
// retains its original cleanup stack and creates no replacement runtime.
func (s *Supervisor) retainSandboxPreparationV1(runtime sandbox) {
	if runtime != nil {
		s.mu.Lock()
		s.sb = runtime
		s.mu.Unlock()
	}
}

// The three concrete preparation runtimes retain their original native
// lifetimes. Process Destroy refuses a live/unreaped init; gvisor Destroy
// requires physical Cmd exit; microvm Destroy rechecks its original VMM exit
// before removing owned storage. A successful Destroy is therefore separate
// from a diagnostic nonzero exit caused by this fixed test's requested stop.
func (s *Supervisor) closeSandboxPrepareRuntimeV1(runtime sandbox, attempted bool) error {
	if runtime == nil {
		return nil
	}
	owned, ok := runtime.(interface{ Destroy() error })
	if !ok {
		return execRefusal("the original preparation runtime has no owned destruction operation")
	}
	s.prepareDestroyOnce.Do(func() {
		if attempted {
			killErr := runtime.Kill()
			waitErr := runtime.Wait()
			if (killErr != nil || waitErr != nil) && s.log != nil {
				s.log.Warn("fixed test stop returned exit diagnostics", "kill", killErr, "wait", waitErr)
			}
		}
		// Stay in the existing supervisor cleanup stack with this SAME
		// runtime and borrowed OP. A failed or uncertain native destruction
		// never becomes an acknowledgement or a background replacement owner.
		for {
			if err := owned.Destroy(); err == nil {
				return
			} else if s.log != nil {
				s.log.Warn("fixed test retains its original destruction owner", "err", err)
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	return nil
}
