package supervisor

import "time"

// active records activity in the session at t, at most once a second.
// The caller holds s.mu.
func (s *Supervisor) active(t time.Time) {
	if t.Sub(s.m.LastActivityAt) >= time.Second {
		s.m.LastActivityAt = t.UTC()
		s.dirty = true
	}
}
