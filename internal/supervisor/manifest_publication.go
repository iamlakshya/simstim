package supervisor

import (
	"fmt"

	"github.com/iamlakshya/simstim/internal/host"
)

func (s *Supervisor) snapshot() host.Manifest {
	m := host.CloneManifest(*s.m)
	if s.decoyUsed && s.rec.phase == host.LifeRunning && m.Status.Source != host.SrcMemory {
		m.Status.Reason = "decoy used"
	}
	if m.Guest != nil {
		g := *m.Guest
		m.Guest = &g
	}
	m.Life = s.rec.life()
	return m
}

// save writes m, a copy of the trusted manifest, unless the last save has
// been made. After it, Delete may remove the session's folder at any
// moment, and a late save would bring it back.
func (s *Supervisor) save(m *host.Manifest, last bool) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	return s.saveLocked(m, last)
}

func (s *Supervisor) saveLocked(m *host.Manifest, last bool) error {
	if s.closed {
		return nil
	}
	if err := s.writeLive(m, last); err != nil {
		s.log.Warn("manifest not saved", "err", err)
		return fmt.Errorf("save the trusted manifest: %w", err)
	}
	return nil
}
