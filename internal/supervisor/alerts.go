package supervisor

import (
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/host"
)

func (s *Supervisor) closeAlerts() {
	s.mu.Lock()
	d := s.alerts
	s.mu.Unlock()
	if d != nil {
		d.Close(2 * time.Second)
	}
}

// makeAlert builds an alert event for the primary session. Caller holds s.mu.
func (s *Supervisor) makeAlert(kind string) alerts.Event {
	return s.makeSessionAlert(s.m, kind)
}

func (s *Supervisor) makeSessionAlert(m *host.Manifest, kind string) alerts.Event {
	return alerts.Event{
		Event:   kind,
		Session: alerts.Session{ID: m.ID, Name: m.Name, Agent: m.Agent.Kind, Dir: m.Cwd},
	}
}

func (s *Supervisor) alertEvent(kind string) {
	if s.alerts != nil {
		s.alerts.Notify(s.makeAlert(kind))
	}
}

func (s *Supervisor) alertMemory(usage string) {
	if s.alerts != nil {
		ev := s.makeAlert(alerts.Memory)
		ev.Usage = usage
		s.alerts.Notify(ev)
	}
}

func (s *Supervisor) alertEnded(code int, sig string, oom bool) {
	if s.alerts == nil {
		return
	}
	kind := alerts.Exited
	switch {
	case oom:
		kind = alerts.OOM
	case code != 0 || sig != "":
		kind = alerts.Failed
	}
	ev := s.makeAlert(kind)
	ev.ExitCode, ev.Signal = &code, sig
	s.alerts.Notify(ev)
}

func (s *Supervisor) noteTyping(p []byte) {
	if s.alerts != nil && host.PersonTyped(p) {
		s.alerts.Typed()
	}
}
