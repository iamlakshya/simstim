//go:build linux

package microvm

import "fmt"

// Called only by final prepared/destroy consumers, after the engine's actual
// original native join. It releases no evidence while a runtime is uncertain.
func (s *sandbox) releaseOriginalEvidenceV1() error {
	s.mu.Lock()
	command, started, physical := s.command, s.started, s.physicalExit
	s.mu.Unlock()
	if !started {
		return s.closePartialSlotOwnersV1()
	}
	if !physical {
		return fmt.Errorf("the original runtime has no positive native closure for final evidence release")
	}
	if err := s.closePartialSlotOwnersV1(); err != nil {
		return err
	}
	if release, ok := command.(interface{ ReleaseOriginalEvidenceV1() error }); ok {
		return release.ReleaseOriginalEvidenceV1()
	}
	return nil
}
