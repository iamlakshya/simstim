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

// maxEarlyEvents caps guest ctl events queued before bootSandbox records the
// trusted start fields, preventing an untrusted guest from growing memory.
const maxEarlyEvents = 64

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
	var childSnaps []*host.Manifest
	for id, sess := range s.sessions {
		if id == s.id || !sess.dirty {
			continue
		}
		sess.dirty = false
		cp := snapshotManifest(sess.m)
		childSnaps = append(childSnaps, &cp)
	}
	s.mu.Unlock()
	if dirty {
		if err := s.save(false); err != nil {
			s.log.Warn("manifest not saved", "err", err)
		}
	}
	s.checkDisk(now)
	s.checkIdleStop(now)
	for _, cp := range childSnaps {
		if err := cp.Save(); err != nil {
			s.log.Warn("child manifest not saved", "session", cp.ID, "err", err)
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
	now := time.Now()
	nowUTC := now.UTC()

	s.mu.Lock()
	if s.starting {
		if len(s.earlyEvents) < maxEarlyEvents {
			s.earlyEvents = append(s.earlyEvents, ev)
		}
		s.mu.Unlock()
		return
	}
	savePrimary, saveChild, foreign := s.applyGuestEventLocked(ev, now, nowUTC)
	s.mu.Unlock()

	if foreign && s.log != nil {
		s.log.Warn("dropped ctl event for foreign session", "event", ev.Type, "session", ev.Session)
		return
	}
	if savePrimary {
		_ = s.save(false)
	}
	if saveChild != nil {
		_ = saveChild.Save()
	}
}

// applyGuestEventLocked applies one validated guest event. Caller holds s.mu.
func (s *Supervisor) applyGuestEventLocked(ev guest.Event, now, nowUTC time.Time) (savePrimary bool, saveChild *host.Manifest, foreign bool) {
	targetID := ev.Session
	if targetID == "" {
		targetID = s.id
	}
	sess := s.sessions[targetID]
	if sess == nil && targetID == s.id && s.m != nil {
		sess = &sandboxSession{id: s.id, m: s.m}
	}
	if sess == nil {
		return false, nil, true
	}
	isPrimary := targetID == s.id
	markDirty := func() {
		if isPrimary {
			s.dirty = true
		} else {
			sess.dirty = true
		}
	}
	switch ev.Type {
	case "heartbeat":
		s.lastHB = now
		s.hbSilent = false
	case "activity":
		sess.m.LastActivityAt = nowUTC
		markDirty()
	case "resize":
		if ev.Cols > 0 && ev.Rows > 0 {
			sess.m.Term.Cols, sess.m.Term.Rows = ev.Cols, ev.Rows
			markDirty()
		}
	case "agent_id":
		if s.applyAgentID(sess, ev, nowUTC) {
			if isPrimary {
				return true, nil, false
			}
			cp := snapshotManifest(sess.m)
			return false, &cp, false
		}
	case "status":
		s.applyGuestStatus(sess, ev, nowUTC)
	case "alert":
		s.applyGuestAlert(sess, ev)
	case "ports":
		s.applyGuestPorts(ev, nowUTC)
	case "mem":
		s.applyGuestMem(ev, nowUTC)
	case "ended":
		if s.applyGuestEnded(sess, ev, nowUTC) {
			if isPrimary {
				return true, nil, false
			}
			cp := snapshotManifest(sess.m)
			return false, &cp, false
		}
	}
	return false, nil, false
}

// applyAgentID checks and records an agent session ID reported by the guest.
// Caller holds s.mu.
func (s *Supervisor) applyAgentID(sess *sandboxSession, ev guest.Event, nowUTC time.Time) bool {
	agent, _ := agents.Find(sess.m.Agent.Kind)
	if err := agent.CheckID(ev.AgentID); err != nil {
		s.log.Warn("session ID from guest not kept", "session", sess.id, "reason", err.Error(), "kind", sess.m.Agent.Kind)
		return false
	}
	if ev.AgentID == sess.m.Agent.AgentSessionID {
		return false
	}
	sess.m.Agent.AgentSessionID = ev.AgentID
	sess.m.Agent.IDSource = "hook"
	sess.m.Agent.IDHistory = append(sess.m.Agent.IDHistory, host.IDEvent{ID: ev.AgentID, Source: ev.IDSource, At: nowUTC})
	if sess.id == s.id {
		s.dirty = true
	} else {
		sess.dirty = true
	}
	return true
}

// applyGuestStatus stores a guest status claim while the sandbox is alive.
// Caller holds s.mu.
func (s *Supervisor) applyGuestStatus(sess *sandboxSession, ev guest.Event, nowUTC time.Time) {
	if s.ended || sess.ended {
		return
	}
	prevState := sess.m.Status.State
	sess.m.Guest = &host.GuestClaims{
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
	if sess.m.Status.Source != host.SrcMemory || ev.State == host.StateNeedsInput {
		sess.m.Status = host.Status{
			State:      ev.State,
			Since:      nowUTC,
			Source:     ev.Source,
			Reason:     ev.Reason,
			FullScreen: ev.FullScreen,
			OOM:        sess.m.Status.OOM,
			Claimed:    true,
		}
		if sess.m.Status.OOM && ev.State != host.StateNeedsInput && ev.Reason == "" {
			sess.m.Status.Reason = api.ReasonChildOOM
		}
	}
	if sess.id == s.id {
		s.dirty = true
	} else {
		sess.dirty = true
	}
	if prevState != ev.State {
		host.RecordWatchEvent(api.WatchEvent{
			Event:   api.EventStateChanged,
			Session: sess.id,
			Name:    sess.m.Name,
			Agent:   sess.m.Agent.Kind,
			State:   ev.State,
			Prev:    prevState,
			Reason:  ev.Reason,
		})
	}
}

// applyGuestAlert forwards an allowlisted agent-side alert. Caller holds s.mu.
func (s *Supervisor) applyGuestAlert(sess *sandboxSession, ev guest.Event) {
	if s.ended || sess.ended {
		return
	}
	switch ev.Kind {
	case alerts.NeedsInput:
		host.RecordWatchEvent(api.WatchEvent{
			Event:   api.EventNeedsInput,
			Session: sess.id,
			Name:    sess.m.Name,
			Agent:   sess.m.Agent.Kind,
			Detail:  ev.Detail,
		})
		if s.alerts != nil {
			ae := s.makeSessionAlert(sess.m, alerts.NeedsInput)
			ae.Detail = ev.Detail
			ae.DetailFrom = "guest"
			s.alerts.Notify(ae)
		}
	case alerts.TurnDone:
		host.RecordWatchEvent(api.WatchEvent{
			Event:   api.EventTurnDone,
			Session: sess.id,
			Name:    sess.m.Name,
			Agent:   sess.m.Agent.Kind,
		})
		if s.alerts != nil && ev.TurnNS > 0 {
			ae := s.makeSessionAlert(sess.m, alerts.TurnDone)
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

// applyGuestMem records the guest's untrusted memory claim. Trusted host
// cgroup memory limits and warnings remain authoritative. Caller holds s.mu.
func (s *Supervisor) applyGuestMem(ev guest.Event, nowUTC time.Time) {
	if s.ended {
		return
	}
	if s.m.Guest == nil {
		s.m.Guest = &host.GuestClaims{}
	}
	s.m.Guest.Mem = &host.GuestMemClaim{
		Used:      ev.MemUsed,
		Total:     ev.MemTotal,
		Available: ev.MemAvailable,
		OOMKills:  ev.OOMKills,
	}
	s.m.Guest.UpdatedAt = nowUTC
	s.dirty = true
}

// applyGuestEnded records an agent exit reported by the untrusted guest as a
// guest claim while the sandbox process itself is still alive. Trusted end,
// OOM reaping, and link_silent teardown happen only when the sandbox exits.
// Caller holds s.mu.
func (s *Supervisor) applyGuestEnded(sess *sandboxSession, ev guest.Event, nowUTC time.Time) bool {
	if s.ended || s.stopping || sess.ended {
		return false
	}
	code := ev.ExitCode
	state := host.StateExited
	if code != 0 || ev.Signal != "" {
		state = host.StateErrored
	}
	sess.m.Guest = &host.GuestClaims{
		State:     state,
		Source:    "guest",
		Since:     nowUTC,
		UpdatedAt: nowUTC,
	}
	if sess.m.Status.Source != host.SrcMemory {
		sess.m.Status = host.Status{
			State:    state,
			Since:    nowUTC,
			ExitCode: &code,
			Signal:   ev.Signal,
			OOM:      sess.m.Status.OOM,
			Source:   "guest",
			Claimed:  true,
		}
	}
	if sess.id == s.id {
		s.dirty = false
	} else {
		sess.dirty = false
	}
	s.audit.Record(AuditEntry{Event: "guest_end", Session: sess.id, ExitCode: &code, Signal: ev.Signal})
	host.RecordWatchEvent(api.WatchEvent{
		Event:    api.EventEnded,
		Session:  sess.id,
		Name:     sess.m.Name,
		Agent:    sess.m.Agent.Kind,
		State:    state,
		ExitCode: &code,
		Signal:   ev.Signal,
	})
	if sess.id == s.id && s.m.RemoveOnExit && s.attached.Load() == 0 && (!s.m.RemoveWaitAttach || s.everAttached.Load()) {
		go s.requestStopKind(stopKindRemoveOnExit, "remove_on_exit", false)
	}
	return true
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
	var downgradeEv *alerts.Event
	if !restored && s.m.Wall != nil && s.m.Wall.DowngradedFrom != "" {
		dev := s.makeAlert(alerts.WallDowngraded)
		dev.Usage = host.WallDowngradeDetail(s.m.Wall)
		downgradeEv = &dev
	}
	s.mu.Unlock()
	if d != nil {
		d.Notify(ev)
		if downgradeEv != nil {
			d.Notify(*downgradeEv)
		}
	}
}

func (s *Supervisor) noteAttach(sessionID string, a proto.Attach) {
	s.mu.Lock()
	if sess := s.sessions[sessionID]; sess != nil {
		sess.m.LastAttachedAt = time.Now().UTC()
		if sessionID == s.id {
			s.mem.NoteAttach(a.Reconnect)
			s.dirty = true
		} else {
			sess.dirty = true
		}
	}
	s.mu.Unlock()
}
