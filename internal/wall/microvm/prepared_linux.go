//go:build linux

package microvm

import (
	"fmt"
)

// ReleasePreparedV1 closes only this factory's native lease after an unstarted
// preparation or completed owned runtime. It preserves its disk and metadata,
// refuses a live runtime, and fences any later Start on a discarded factory.
func (s *sandbox) ReleasePreparedV1() error {
	s.destroyMu.Lock()
	defer s.destroyMu.Unlock()
	s.mu.Lock()
	s.preparationClosing = true
	if s.started {
		s.mu.Unlock()
		if !s.refreshPhysicalExit() {
			return fmt.Errorf("the live or unproven microvm still owns its prepared resource lease")
		}
		s.mu.Lock()
	}
	s.mu.Unlock()
	if err := s.closeSnapshotDiskV1(); err != nil {
		return err
	}
	if err := s.releaseOriginalEvidenceV1(); err != nil {
		return err
	}
	s.mu.Lock()
	s.preparedReleased = true
	s.mu.Unlock()
	if err := s.preparationFileCloseErrorV1(); err != nil {
		return err
	}
	if err := s.releaseBootStackV2(); err != nil {
		return err
	}
	return s.closePreparationRootV1()
}

func (s *sandbox) closePreparationRootV1() error {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	s.mu.Lock()
	root := s.preparedRoot
	s.mu.Unlock()
	if root == nil {
		return nil
	}
	if err := root.Close(); err != nil {
		s.mu.Lock()
		s.rootCloseErr = err
		s.mu.Unlock()
		return err
	}
	s.mu.Lock()
	s.preparedRoot = nil
	s.rootCloseErr = nil
	s.mu.Unlock()
	return nil
}
