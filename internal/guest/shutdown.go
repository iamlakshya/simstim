package guest

import (
	"syscall"
	"time"
)

func (r *runner) signalStop() {
	r.stopOnce.Do(func() { close(r.stopCh) })
}

// beginShutdown claims shutdownOnce synchronously with grace and runs holder
// termination asynchronously so callers can reply before holders finish exiting
// without racing a later SIGTERM handler.
func (r *runner) beginShutdown(grace time.Duration) {
	r.shutdownOnce.Do(func() {
		go func() {
			defer close(r.shutdownDone)
			r.stopHolders(grace)
			r.saveKeepFiles()
			r.signalStop()
		}()
	})
}

func (r *runner) shutdown(grace time.Duration) {
	r.beginShutdown(grace)
	<-r.shutdownDone
}

func (r *runner) stopHolders(grace time.Duration) {
	r.mu.Lock()
	list := make([]*managedHolder, 0, len(r.holders))
	for _, mh := range r.holders {
		list = append(list, mh)
	}
	r.mu.Unlock()

	for _, mh := range list {
		if mh.cmd != nil && mh.cmd.Process != nil {
			_ = mh.cmd.Process.Signal(syscall.SIGTERM)
		}
	}
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	for _, mh := range list {
		select {
		case <-mh.done:
		case <-deadline.C:
			if mh.cmd != nil && mh.cmd.Process != nil {
				_ = mh.cmd.Process.Kill()
			}
			if mh.agentPID > 1 && r.opts.MicroVM {
				killAgentGroup(mh.agentPID)
			}
			<-mh.done
		}
	}
}
