//go:build linux

package microvm

import "fmt"

// ReleasePreparedV1 closes only this factory's native lease after an unstarted
// preparation or completed owned runtime. It preserves its disk and metadata,
// refuses a live runtime, and fences any later Start on a discarded factory.
func (s *sandbox) ReleasePreparedV1() error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		if !s.refreshPhysicalExit() {
			return fmt.Errorf("the live or unproven microvm still owns its prepared resource lease")
		}
		s.mu.Lock()
	}
	s.preparedReleased = true
	lease := s.lease
	s.lease = nil
	s.mu.Unlock()
	if lease != nil {
		return lease.Close()
	}
	return nil
}
