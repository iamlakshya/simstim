package supervisor

import (
	"encoding/json"
	"regexp"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

// The guest's events are claims (spec 3.4, N8). The supervisor checks each
// one, cleans its strings, and keeps it in the trusted manifest apart from
// its own facts: the row shows the state as claimed. A claim can change
// the shown state, raise needs_input, turn_done, exited or failed, and
// give the agent's own session ID, which must have the agent's form. It
// can never change the session's level, limits, socket or record of the
// run, never ends the run, and never makes a kill at the memory cap: those
// come from the trusted side only.

// The caps on a claim's strings (spec 10.3).
const (
	maxReason  = 200
	maxDetail  = 500
	maxSignal  = 32
	maxHistory = 100 // the agent IDs that the manifest keeps
)

// sourceRE is the form of a status claim's source.
var sourceRE = regexp.MustCompile(`^[a-z0-9:._-]{1,64}$`)

// claimStates are the states that a status claim may give. The end comes
// with ended, and the rest only from the trusted side.
var claimStates = map[string]bool{host.StateWorking: true, host.StateIdle: true, host.StateNeedsInput: true}

// claimAlerts are the alerts that the guest may raise. memory comes from
// the trusted side, from the unit's own memory files.
var claimAlerts = map[string]bool{alerts.NeedsInput: true, alerts.TurnDone: true}

// claim is one event from the guest, checked and cleaned.
type claim struct {
	runToken     string
	event        string
	state        string
	source       string
	reason       string
	fullScreen   bool
	kind         string
	detail       string
	id           string
	exitCode     int
	signal       string
	captureNonce string
}

// cleanClaim cleans a string from the guest, and cuts it to n runes.
func cleanClaim(s string, n int) string { return api.CleanText(s, n) }

// parseEvent checks one event of session's sandbox, whose agent is of
// kind. It reports false for an event to drop: one that does not parse,
// that names another session, or that breaks a rule of spec 10.3.
func parseEvent(p []byte, session, kind string) (claim, bool) {
	var ev proto.CtlEvent
	if len(p) > maxEvent || json.Unmarshal(p, &ev) != nil {
		return claim{}, false
	}
	if len(ev.RunToken) > 128 {
		return claim{}, false
	}
	c := claim{event: ev.Event, runToken: ev.RunToken}
	if ev.Event == proto.EventHeartbeat {
		return c, true
	}
	// Every other event is about one session of the sandbox.
	if ev.Session != session {
		return claim{}, false
	}
	switch ev.Event {
	case proto.EventStatus:
		if !claimStates[ev.State] || !sourceRE.MatchString(ev.Source) {
			return claim{}, false
		}
		c.state, c.source, c.reason, c.fullScreen = ev.State, ev.Source, cleanClaim(ev.Reason, maxReason), ev.FullScreen
	case proto.EventAlert:
		if !claimAlerts[ev.Kind] {
			return claim{}, false
		}
		c.kind, c.detail = ev.Kind, cleanClaim(ev.Detail, maxDetail)
	case proto.EventAgentID:
		agent, ok := agents.Find(kind)
		if !ok || agent.CheckID(ev.ID) != nil {
			return claim{}, false
		}
		c.id = ev.ID
	case proto.EventStateReady:
		if len(ev.CaptureNonce) != 32 {
			return claim{}, false
		}
		for _, r := range ev.CaptureNonce {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
				return claim{}, false
			}
		}
		c.captureNonce = ev.CaptureNonce
	case proto.EventEnded:
		if ev.ExitCode == nil || *ev.ExitCode < 0 || *ev.ExitCode > 255 {
			return claim{}, false
		}
		c.exitCode, c.signal = *ev.ExitCode, cleanClaim(ev.Signal, maxSignal)
	default:
		return claim{}, false
	}
	return c, true
}

// event takes one event from the ctl stream.
//
// Events that come before the answer to start wait until the run is
// recorded (started), so that each applies to the run that it is about, in
// order. They count against maxEarlyBytes, and more are dropped, except
// the first valid end of the agent: it has room of its own, of at most
// maxEvent bytes.
func (s *Supervisor) event(p []byte) {
	if s.memberEvent(p) {
		return
	}
	s.evMu.Lock()
	defer s.evMu.Unlock()
	if !s.ready {
		fits := s.earlyBytes+len(p) <= maxEarlyBytes
		if !fits && !s.earlyEnd && validEnd(p, s.id) {
			fits, s.earlyEnd = true, true
		}
		if !fits && !s.earlyCapture && s.validCaptureMessage(p) {
			fits, s.earlyCapture = true, true
		}
		if fits {
			s.early = append(s.early, append([]byte(nil), p...))
			s.earlyBytes += len(p)
		}
		return
	}
	s.apply(p)
}

// validEnd reports whether p is an end of the agent that parseEvent takes
// for session id: one with the session and an exit code.
func validEnd(p []byte, id string) bool {
	c, ok := parseEvent(p, id, "")
	return ok && c.event == proto.EventEnded
}

// maxEarlyBytes caps the events that wait for the answer to start. It is
// small beside the link's own bound of 16 MiB (spec 5.2 and 20.10).
const maxEarlyBytes = 256 << 10

// replay marks the run as recorded, and applies the events that waited for
// it.
func (s *Supervisor) replay() {
	s.evMu.Lock()
	defer s.evMu.Unlock()
	s.ready = true
	for _, p := range s.early {
		s.apply(p)
	}
	s.early, s.earlyBytes = nil, 0
}

// apply applies one event. The caller holds s.evMu.
func (s *Supervisor) apply(p []byte) {
	s.mu.Lock()
	kind := s.m.Agent.Kind
	s.mu.Unlock()
	c, ok := parseEvent(p, s.id, kind)
	if !ok {
		s.log.Debug("dropped an event from the guest", "bytes", len(p))
		return
	}
	switch c.event {
	case proto.EventHeartbeat:
		// Only a valid heartbeat says that the guest init is alive: other
		// events, valid or not, never hide a silence.
		s.mu.Lock()
		s.lastBeat = now()
		s.heartbeatDelay = 0
		if s.silent {
			s.silent = false
			ev := s.alertEvent(alerts.HealthRecovered)
			ev.Reason = "heartbeat_resumed"
			s.raise(ev)
		}
		s.mu.Unlock()
	case proto.EventStatus:
		s.claimStatus(c)
	case proto.EventAlert:
		s.claimAlert(c)
	case proto.EventAgentID:
		s.claimAgentID(c)
	case proto.EventEnded:
		s.claimEnded(c)
	case proto.EventStateReady:
		s.readyCapture(c.captureNonce)
	}
}

// claimStatus keeps a status claim, and shows it unless the trusted side's
// memory warning takes the session's status.
func (s *Supervisor) claimStatus(c claim) {
	t := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.end.claimed || s.ended() || s.m.Guest == nil {
		return
	}
	g := s.m.Guest
	prev := g.State
	if prev == "" {
		prev = host.StateWorking // a session starts out working
	}
	g.State, g.Source, g.Reason, g.FullScreen, g.At = c.state, c.source, c.reason, c.fullScreen, t.UTC()
	s.dirty = true
	if prev != c.state {
		ev := s.alertEvent(alerts.StateChanged)
		ev.Claimed = true
		ev.Reason = c.state
		ev.Summary = "state changed to " + c.state + " (claimed)"
		s.raise(ev)
		s.turn.change(prev, c.state, t)
		if s.al != nil {
			s.al.StateChanged()
		}
	}
	s.showStatus(t)
}

// showStatus sets the session's status from the guest's last claims and
// the trusted facts: the memory warning and the kills at the cap, as the
// holder's updateStatus does. The memory warning shows over a claimed end
// too. The caller holds s.mu.
func (s *Supervisor) showStatus(t time.Time) {
	if s.ended() || s.m.Guest == nil {
		return
	}
	old := s.m.Status
	st := s.claimedStatus()
	networkPending, networkStopped, networkReason := s.pendingNetworkStatus()
	if st.ExitCode == nil {
		// While the agent runs, the mark says that the cap stopped a
		// process in the session. After its end, the mark means the agent
		// itself, as for a holder (claimedStatus).
		st.OOM = s.rec.oom
	}
	switch {
	case s.mark.on && st.State != host.StateNeedsInput:
		st = host.Status{State: host.StateNeedsInput, Source: host.SrcMemory, Reason: host.MemoryReason(s.mark.usage), OOM: st.OOM}
	case networkPending && !s.mark.on && st.ExitCode == nil:
		st = host.Status{State: host.StateNeedsInput, Source: "network", Reason: networkReason, OOM: st.OOM}
	case st.OOM && st.ExitCode == nil && st.State != host.StateNeedsInput:
		// The memory cap stopped a process that the agent started.
		st.Reason = host.OOMChildReason
	}
	if networkStopped && !networkPending && !s.mark.on && st.ExitCode == nil && st.State != host.StateNeedsInput {
		st.Reason = "unknown network requests stopped after repeated refusals"
	}
	st.Since = old.Since
	if st.State != old.State || st.Source != old.Source {
		st.Since = t.UTC()
	}
	if !sameStatus(st, old) {
		if st.Source == host.SrcMemory && old.Source != host.SrcMemory {
			s.mark.shown = t
		}
		s.m.Status = st
		s.dirty = true
	}
}

// claimedStatus is the status that the guest's claims give: its claimed
// end, its last state, or working at the start. The caller holds s.mu.
func (s *Supervisor) claimedStatus() host.Status {
	g := s.m.Guest
	switch {
	case g.ExitCode != nil:
		code := *g.ExitCode
		st := host.Status{State: host.StateExited, ExitCode: &code, Signal: g.Signal, Source: "process", Claimed: true,
			OOM: s.rec.oomAgent}
		switch {
		case s.rec.oomAgent:
			st.State, st.Reason = host.StateErrored, "oom"
		case code != 0:
			st.State = host.StateErrored
		}
		if s.rec.oom && !s.rec.oomAgent {
			st.Reason = host.OOMChildReason
		}
		return st
	case g.State != "":
		return host.Status{State: g.State, Source: g.Source, Reason: g.Reason, FullScreen: g.FullScreen, Claimed: true}
	}
	return host.Status{State: host.StateWorking, Source: "start"}
}

// sameStatus reports whether a and b say the same, their exit codes by
// value.
func sameStatus(a, b host.Status) bool {
	ac, bc := a.ExitCode, b.ExitCode
	a.ExitCode, b.ExitCode = nil, nil
	if a != b || (ac == nil) != (bc == nil) {
		return false
	}
	return ac == nil || *ac == *bc
}

// claimAlert raises needs_input or turn_done, marked as claimed. Its detail
// is the guest's text, and a destination learns so (alerts.Event.DetailFrom).
func (s *Supervisor) claimAlert(c claim) {
	t := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.end.claimed || s.ended() {
		return
	}
	ev := s.alertEvent(c.kind)
	ev.Claimed = true // the guest raised it
	switch c.kind {
	case alerts.NeedsInput:
		ev.Detail, ev.DetailFrom = c.detail, alerts.FromGuest
	case alerts.TurnDone:
		ev.Turn = s.turn.take(t)
		s.queueCheckpointV1(checkpointTurnV1{session: s.id, detail: c.detail})
	}
	s.raise(ev)
}

// claimAgentID keeps the agent's own session ID, which parseEvent checked
// against the agent's form, for its resume command.
func (s *Supervisor) claimAgentID(c claim) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := &s.m.Agent
	if c.id == a.AgentSessionID {
		return
	}
	a.AgentSessionID, a.IDSource = c.id, "hook"
	a.IDHistory = append(a.IDHistory, host.IDEvent{ID: c.id, Source: "guest", At: time.Now().UTC()})
	if n := len(a.IDHistory); n > maxHistory {
		a.IDHistory = append([]host.IDEvent(nil), a.IDHistory[n-maxHistory:]...)
	}
	s.dirty = true
}

// alertEvent starts an alert about this session, from the trusted
// manifest. The caller holds s.mu.
func (s *Supervisor) alertEvent(kind string) alerts.Event {
	m := s.m
	return alerts.Event{Event: kind, Session: alerts.Session{ID: s.id, Name: m.Name, Agent: m.Agent.Kind, Dir: m.Cwd}}
}

// raise sends an alert, once the dispatcher has started. The caller holds
// s.mu.
func (s *Supervisor) raise(ev alerts.Event) {
	if s.al != nil {
		s.al.Notify(ev)
	}
}
