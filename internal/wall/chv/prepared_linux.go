//go:build linux

package chv

import "fmt"

// ReleasePreparedV1 closes only this factory's native lease after an unstarted
// preparation or completed owned runtime. It preserves its disk and metadata,
// refuses a live runtime, and fences any later Start on a discarded factory.
func (s *sandbox) ReleasePreparedV1() error {
	s.mu.Lock()
	if s.started {
		select {
		case <-s.done:
		default:
			s.mu.Unlock()
			return fmt.Errorf("the live microvm still owns its prepared resource lease")
		}
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
