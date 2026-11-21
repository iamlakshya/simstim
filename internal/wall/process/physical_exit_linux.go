//go:build linux

package process

import "errors"

// PhysicalExitV1 requires this SAME actual creator's native completion, its
// atomic launcher pidfd exit and the original copy-owner/publisher joins.
// Launcher exit, a missing init handoff or a closed flag alone prove nothing.
func (s *sandbox) PhysicalExitV1() bool {
	if s == nil {
		return false
	}
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	s.mu.Lock()
	creator, copyOwner, freezer := s.creator, s.copy, s.freezer
	ended := s.cmd != nil && s.cmd.ProcessState != nil && s.pidfd < 0
	s.mu.Unlock()
	if !ended {
		return false
	}
	proven, err := creator.check()
	if err != nil || !proven {
		return false
	}
	// A failed Start may have observed the final native completion only after
	// its first cleanup turn. Retry the SAME original resource owners.
	if copyOwner != nil {
		if err := copyOwner.native.nativeExit(); err != nil {
			s.mu.Lock()
			s.result.Err = errors.Join(s.result.Err, err)
			s.mu.Unlock()
			return false
		}
	}
	if freezer != nil {
		if err := freezer.closeEmpty(); err != nil {
			s.mu.Lock()
			s.result.Err = errors.Join(s.result.Err, err)
			s.mu.Unlock()
			return false
		}
	}
	err = creator.close()
	s.mu.Lock()
	s.result.Err = errors.Join(s.result.Err, err)
	if err == nil {
		s.closed = true
	}
	s.mu.Unlock()
	return err == nil
}
