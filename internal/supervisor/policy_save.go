package supervisor

// failPolicySave revokes live doors after an unrecorded policy change. It must
// not join the egress event worker while its caller retains polMu: that worker
// needs the same turn to finish its already received lease-end records.
func (s *Supervisor) failPolicySave(ep *egressProc) {
	s.mu.Lock()
	s.m.Armed = false
	if disarmPublicSecretsLocked(s.m) {
		s.dirty = true
	}
	s.mu.Unlock()
	if ep != nil {
		if ep.ctl != nil {
			ep.ctl.closeOwnedControl()
		} else if ep.sess != nil {
			_ = ep.sess.Close()
		}
	}
	s.closePortListeners(true)
}
