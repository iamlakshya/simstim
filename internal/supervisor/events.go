package supervisor

import (
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (s *Supervisor) tickLoop() {
	defer s.wg.Done()
	t := time.NewTicker(s.opt.TickInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.step()
		case <-s.doneCh:
			return
		}
	}
}

func (s *Supervisor) step() {
	var sample host.MemSample
	if dir := s.mem.Dir(); dir != "" {
		sample = host.ReadUnitMemSample(dir)
	}
	now := time.Now()
	s.mu.Lock()
	var typedAt time.Time
	if s.alerts != nil {
		typedAt = s.alerts.TypedAt()
	}
	memRes := s.mem.Step(sample, s.m.Status.Source == host.SrcMemory, typedAt)
	if memRes.Warned {
		s.alertMemory(memRes.Usage)
	}
	if memRes.ChildOOM {
		s.m.Status.OOM = true
		if s.m.Status.State != host.StateNeedsInput {
			s.m.Status.Reason = api.ReasonChildOOM
		}
		s.dirty = true
		s.alertEvent(alerts.OOM)
	}
	if !s.ended {
		s.reconcileStatus(memRes)
	}
	if !s.ended && !s.stopping && !s.hbSilent && now.Sub(s.lastHB) > s.opt.HeartbeatTimeout {
		s.hbSilent = true
		s.log.Warn("guest link silent", "since", now.Sub(s.lastHB))
		s.audit.Record(AuditEntry{Event: "link_silent", Session: s.id})
		s.alertEvent(alerts.LinkSilent)
	}
	dirty := s.dirty
	s.dirty = false
	var snap host.Manifest
	if dirty {
		snap = *s.m
	}
	s.mu.Unlock()
	if dirty {
		if err := s.save(&snap, false); err != nil {
			s.log.Warn("manifest not saved", "err", err)
		}
	}
}

// reconcileStatus combines trusted memory warnings with guest claims.
// Caller holds s.mu.
func (s *Supervisor) reconcileStatus(memRes host.MemTickResult) {
	if memRes.Mark {
		if s.m.Status.State != host.StateNeedsInput || s.m.Status.Source != host.SrcMemory || s.m.Status.Reason != memRes.Reason {
			// Agent's own needs_input takes precedence over memory warning.
			if s.m.Guest == nil || s.m.Guest.State != host.StateNeedsInput {
				s.m.Status = host.Status{
					State:      host.StateNeedsInput,
					Since:      time.Now().UTC(),
					Source:     host.SrcMemory,
					Reason:     memRes.Reason,
					FullScreen: s.m.Status.FullScreen,
					OOM:        s.m.Status.OOM,
					Claimed:    false,
				}
				s.dirty = true
			}
		}
		return
	}
	if s.m.Status.Source == host.SrcMemory && s.m.Guest != nil && s.m.Guest.State != "" {
		s.m.Status = host.Status{
			State:      s.m.Guest.State,
			Since:      s.m.Guest.Since,
			Source:     s.m.Guest.Source,
			Reason:     s.m.Guest.Reason,
			FullScreen: s.m.Guest.FullScreen,
			OOM:        s.m.Status.OOM,
			Claimed:    true,
		}
		s.dirty = true
	}
}

func (s *Supervisor) applyGuestEvent(ev guest.Event) {
	if ev.Session != "" && ev.Session != s.id {
		s.log.Warn("dropped ctl event for foreign session", "event", ev.Type, "session", ev.Session)
		return
	}
	now := time.Now()
	nowUTC := now.UTC()
	var saveCopy *host.Manifest

	s.mu.Lock()
	switch ev.Type {
	case "heartbeat":
		s.lastHB = now
		s.hbSilent = false
	case "activity":
		s.m.LastActivityAt = nowUTC
		s.dirty = true
	case "resize":
		if ev.Cols > 0 && ev.Rows > 0 {
			s.m.Term.Cols, s.m.Term.Rows = ev.Cols, ev.Rows
			s.dirty = true
		}
	case "agent_id":
		saveCopy = s.applyAgentID(ev, nowUTC)
	case "status":
		s.applyGuestStatus(ev, nowUTC)
	case "alert":
		s.applyGuestAlert(ev)
	case "ports":
		s.applyGuestPorts(ev, nowUTC)
	case "ended":
		saveCopy = s.applyGuestEnded(ev, nowUTC)
	}
	s.mu.Unlock()

	if saveCopy != nil {
		_ = s.save(saveCopy, false)
	}
}

// applyAgentID checks and records an agent session ID reported by the guest.
// Caller holds s.mu.
func (s *Supervisor) applyAgentID(ev guest.Event, nowUTC time.Time) *host.Manifest {
	agent, _ := agents.Find(s.m.Agent.Kind)
	if err := agent.CheckID(ev.AgentID); err != nil {
		s.log.Warn("session ID from guest not kept", "reason", err.Error(), "kind", s.m.Agent.Kind)
		return nil
	}
	if ev.AgentID == s.m.Agent.AgentSessionID {
		return nil
	}
	s.m.Agent.AgentSessionID = ev.AgentID
	s.m.Agent.IDSource = "hook"
	s.m.Agent.IDHistory = append(s.m.Agent.IDHistory, host.IDEvent{ID: ev.AgentID, Source: ev.IDSource, At: nowUTC})
	s.dirty = true
	cp := *s.m
	return &cp
}

// applyGuestStatus stores a guest status claim while the sandbox is alive.
// Caller holds s.mu.
func (s *Supervisor) applyGuestStatus(ev guest.Event, nowUTC time.Time) {
	if s.ended {
		return
	}
	s.m.Guest = &host.GuestClaims{
		State:      ev.State,
		Source:     ev.Source,
		Reason:     ev.Reason,
		FullScreen: ev.FullScreen,
		Since:      nowUTC,
		UpdatedAt:  nowUTC,
	}
	if s.alerts != nil {
		s.alerts.StateChanged()
	}
	if s.m.Status.Source != host.SrcMemory || ev.State == host.StateNeedsInput {
		s.m.Status = host.Status{
			State:      ev.State,
			Since:      nowUTC,
			Source:     ev.Source,
			Reason:     ev.Reason,
			FullScreen: ev.FullScreen,
			OOM:        s.m.Status.OOM,
			Claimed:    true,
		}
		if s.m.Status.OOM && ev.State != host.StateNeedsInput && ev.Reason == "" {
			s.m.Status.Reason = api.ReasonChildOOM
		}
	}
	s.dirty = true
}

// applyGuestAlert forwards an allowlisted agent-side alert. Caller holds s.mu.
func (s *Supervisor) applyGuestAlert(ev guest.Event) {
	if s.ended || s.alerts == nil {
		return
	}
	switch ev.Kind {
	case alerts.NeedsInput:
		ae := s.makeAlert(alerts.NeedsInput)
		ae.Detail = ev.Detail
		s.alerts.Notify(ae)
	case alerts.TurnDone:
		if ev.TurnNS > 0 {
			ae := s.makeAlert(alerts.TurnDone)
			ae.Turn = time.Duration(ev.TurnNS)
			s.alerts.Notify(ae)
		}
	}
}

// applyGuestPorts records claimed listening ports. Caller holds s.mu.
func (s *Supervisor) applyGuestPorts(ev guest.Event, nowUTC time.Time) {
	if s.m.Guest == nil {
		s.m.Guest = &host.GuestClaims{}
	}
	s.m.Guest.Ports = ev.Ports
	s.m.Guest.UpdatedAt = nowUTC
	s.dirty = true
}

// applyGuestEnded records an agent exit reported by the untrusted guest as a
// guest claim while the sandbox process itself is still alive. Trusted end,
// OOM reaping, and link_silent teardown happen only when the sandbox exits.
// Caller holds s.mu.
func (s *Supervisor) applyGuestEnded(ev guest.Event, nowUTC time.Time) *host.Manifest {
	if s.ended || s.stopping {
		return nil
	}
	code := ev.ExitCode
	state := host.StateExited
	if code != 0 || ev.Signal != "" {
		state = host.StateErrored
	}
	s.m.Guest = &host.GuestClaims{
		State:     state,
		Source:    "guest",
		Since:     nowUTC,
		UpdatedAt: nowUTC,
	}
	if s.m.Status.Source != host.SrcMemory {
		s.m.Status = host.Status{
			State:    state,
			Since:    nowUTC,
			ExitCode: &code,
			Signal:   ev.Signal,
			OOM:      s.m.Status.OOM,
			Source:   "guest",
			Claimed:  true,
		}
	}
	s.dirty = false
	s.audit.Record(AuditEntry{Event: "guest_end", Session: s.id, ExitCode: &code, Signal: ev.Signal})
	cp := *s.m
	return &cp
}

func (s *Supervisor) initAlerts() {
	home, _ := os.UserHomeDir()
	s.mu.Lock()
	s.alerts = alerts.New(alerts.Options{
		Path:   s.opt.AlertsPath,
		Env:    os.Environ(),
		Dir:    home,
		Logger: s.log,
	})
	s.mu.Unlock()
}

func (s *Supervisor) notifyStarted(restored bool) {
	s.mu.Lock()
	d := s.alerts
	kind := alerts.Started
	if restored {
		kind = alerts.Restored
	}
	ev := s.makeAlert(kind)
	s.mu.Unlock()
	if d != nil {
		d.Notify(ev)
	}
}

func (s *Supervisor) closeAlerts() {
	s.mu.Lock()
	d := s.alerts
	s.mu.Unlock()
	if d != nil {
		d.Close(2 * time.Second)
	}
}

// makeAlert builds an alert event. Caller holds s.mu.
func (s *Supervisor) makeAlert(kind string) alerts.Event {
	return alerts.Event{
		Event:   kind,
		Session: alerts.Session{ID: s.id, Name: s.m.Name, Agent: s.m.Agent.Kind, Dir: s.m.Cwd},
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

func (s *Supervisor) noteAttach(a proto.Attach) {
	s.mu.Lock()
	s.m.LastAttachedAt = time.Now().UTC()
	s.mem.NoteAttach(a.Reconnect)
	s.dirty = true
	s.mu.Unlock()
}
