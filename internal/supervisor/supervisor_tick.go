package supervisor

import (
	"github.com/iamlakshya/simstim/internal/host"
	"time"
)

// snapshot is a copy of the trusted manifest, to save or send after the
// caller lets s.mu go, with the record of the run as its Life. It copies
// the claims too: claims change in place. The caller holds s.mu.
// tick saves the trusted manifest when it changed, reads the unit's memory
// and watches the guest init's heartbeat, once a second, until the link
// ends.
func (s *Supervisor) tick() {
	defer s.wg.Done()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-s.link.Done():
			return
		}
		s.saveMembers()
		s.memoryTick()
		s.watchBeat()
		if n := s.ctl.drops(time.Now()); n > 0 {
			s.audit.write("events_dropped", map[string]any{"count": n})
		}
		s.mu.Lock()
		if s.m.Status.State == host.StateWorking {
			// The agent works: the host is not idle (idle.go).
			s.active(time.Now())
		}
		dirty := s.dirty
		s.dirty = false
		m := s.snapshot()
		s.mu.Unlock()
		if dirty {
			_ = s.save(&m, false)
		}
	}
}

// active records activity in the session at t, at most once a second.
// The caller holds s.mu.
func (s *Supervisor) active(t time.Time) {
	if t.Sub(s.m.LastActivityAt) >= time.Second {
		s.m.LastActivityAt = t.UTC()
		s.dirty = true
	}
}
