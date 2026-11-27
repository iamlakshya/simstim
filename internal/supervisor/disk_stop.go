package supervisor

import (
	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/host"
)

// requestDiskStop is the nonblocking callback for this exact runtime owner.
// It never joins the watcher, closes borrowed roots or performs filesystem IO.
func (s *Supervisor) requestDiskStop(generation string, reason error) {
	if reason == nil {
		return
	}
	s.mu.Lock()
	if s.lifetime != nil && s.lifetime.Err() != nil {
		s.mu.Unlock()
		return
	}
	if generation == "" || generation != s.generation || s.stopIntent || s.ended() || s.rec.phase == host.LifeEnding || s.diskFailure != nil {
		s.mu.Unlock()
		return
	}
	s.diskFailure = reason
	s.captureFailed, s.stopIntent, s.dirty = true, true, true
	cancel := s.bootCancel
	if s.bootContextGeneration != generation {
		cancel = nil
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel(reason)
		return
	}
	select {
	case s.quit <- reason.Error():
	default:
	}
}
func (s *Supervisor) raiseDiskFailure() {
	s.mu.Lock()
	if s.diskFailure == nil || s.diskAlerted {
		s.mu.Unlock()
		return
	}
	s.diskAlerted = true
	event := s.alertEvent(alerts.DiskFull)
	event.Usage = s.diskFailure.Error()
	s.mu.Unlock()
	s.raise(event)
}
func (s *Supervisor) diskStopReason() error { s.mu.Lock(); defer s.mu.Unlock(); return s.diskFailure }
