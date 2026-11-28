package supervisor

import (
	"fmt"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
)

func (s *Supervisor) maxRunDuration() time.Duration {
	if s.opt.MaxRun > 0 {
		return s.opt.MaxRun
	}
	if s.m != nil && s.m.Limits.MaxRunSeconds > 0 {
		return time.Duration(s.m.Limits.MaxRunSeconds) * time.Second
	}
	return 0
}

// startMaxRunTimer arms sandbox.max_run for this sandbox start (spec 17.6).
// When the limit elapses, the supervisor stops the sandbox, keeps its work,
// and raises sandbox_stopped with reason "max_run".
func (s *Supervisor) startMaxRunTimer() func() {
	d := s.maxRunDuration()
	if d <= 0 {
		return func() {}
	}
	t := time.AfterFunc(d, func() {
		s.log.Info("sandbox reached max_run, stopping", "max_run", d)
		s.requestStopKind(stopKindMaxRun, "max_run", true)
	})
	return func() { t.Stop() }
}

func (s *Supervisor) recordWatchEvent(ev api.WatchEvent) {
	s.mu.Lock()
	if s.m != nil {
		if ev.Session == "" {
			ev.Session = s.id
		}
		if ev.Name == "" {
			ev.Name = s.m.Name
		}
		if ev.Agent == "" {
			ev.Agent = s.m.Agent.Kind
		}
	}
	s.mu.Unlock()
	host.RecordWatchEvent(ev)
}

func (s *Supervisor) emitSandboxReady() {
	s.mu.Lock()
	if s.alerts != nil {
		s.alerts.Notify(s.makeAlert(alerts.SandboxReady))
	}
	s.mu.Unlock()
	s.recordWatchEvent(api.WatchEvent{
		Event: api.EventSandboxReady,
		State: host.StateWorking,
	})
}

func (s *Supervisor) emitSandboxStopped(reason string) {
	s.mu.Lock()
	if s.alerts != nil {
		ev := s.makeAlert(alerts.SandboxStopped)
		ev.Reason = reason
		s.alerts.Notify(ev)
	}
	s.mu.Unlock()
	s.recordWatchEvent(api.WatchEvent{
		Event:  api.EventSandboxStopped,
		State:  host.StateStopped,
		Reason: reason,
	})
}

// emitSandboxErrorLocked emits sandbox_error to alerts and the host event
// stream. Caller holds s.mu.
func (s *Supervisor) emitSandboxErrorLocked(reason string, code *int, sig string) {
	if s.alerts != nil {
		ev := s.makeAlert(alerts.SandboxError)
		ev.Reason = reason
		ev.ExitCode = code
		ev.Signal = sig
		s.alerts.Notify(ev)
	}
	host.RecordWatchEvent(api.WatchEvent{
		Event:    api.EventSandboxError,
		Session:  s.id,
		Name:     s.m.Name,
		Agent:    s.m.Agent.Kind,
		State:    host.StateErrored,
		Reason:   reason,
		ExitCode: code,
		Signal:   sig,
	})
}

// emitStartError raises sandbox_error when egress or sandbox boot fails before
// the supervisor's long-lived alert dispatcher has started.
func (s *Supervisor) emitStartError(reason string) {
	code := 127
	s.mu.Lock()
	ev := s.makeAlert(alerts.SandboxError)
	ev.Reason = reason
	ev.ExitCode = &code
	name, agentKind := s.m.Name, s.m.Agent.Kind
	s.mu.Unlock()

	host.RecordWatchEvent(api.WatchEvent{
		Event:    api.EventSandboxError,
		Session:  s.id,
		Name:     name,
		Agent:    agentKind,
		State:    host.StateErrored,
		Reason:   reason,
		ExitCode: &code,
	})

	home, _ := os.UserHomeDir()
	d := alerts.New(alerts.Options{
		Path:   s.opt.AlertsPath,
		Env:    os.Environ(),
		Dir:    home,
		Logger: s.log,
	})
	d.Notify(ev)
	d.Close(2 * time.Second)
}

func (s *Supervisor) emitEgressWatchEvent(ev egress.AuditEvent) {
	target := ev.Host
	if ev.Port > 0 {
		target = fmt.Sprintf("%s:%d", ev.Host, ev.Port)
	}
	switch {
	case ev.Decision == "hold" || ev.Rule == "held":
		s.mu.Lock()
		if s.alerts != nil {
			ae := s.makeAlert(alerts.EgressBlocked)
			ae.Reason = target
			s.alerts.Notify(ae)
		}
		s.mu.Unlock()
		s.recordWatchEvent(api.WatchEvent{
			Event:  api.EventEgressHeld,
			Target: target,
			Reason: ev.Why,
		})
	case ev.Decision == string(egress.ActionDeny) && ev.Rule != "model_budget":
		s.mu.Lock()
		if s.alerts != nil {
			ae := s.makeAlert(alerts.EgressBlocked)
			if ev.Why != "" {
				ae.Reason = ev.Why
			} else {
				ae.Reason = target
			}
			s.alerts.Notify(ae)
		}
		s.mu.Unlock()
	case ev.Decision == string(egress.ActionAllow) && ev.Rule == "classifier":
		s.recordWatchEvent(api.WatchEvent{
			Event:  api.EventClassifierAllow,
			Target: target,
			Reason: ev.Why,
		})
	}
}
