package supervisor

import (
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/host"
)

// The supervisor keeps one record of the session's run: its phase, the
// deadline of its boot, the memory cap's kill count from before the guest
// started, the kills that no tick took yet, and the run's end with its
// kind. Only the supervisor's own facts write it. The guest's words live
// in the manifest's Guest, and never reach it. Every reader derives from
// it: the manifest's Life, which restore and the wait of New read, the
// row's last state, the end frame that a client gets, and the end's alerts.

// record is the supervisor's record of the session's run.
type record struct {
	phase   string
	bootBy  time.Time
	started time.Time
	// mem watches the unit's memory files. It begins before the guest
	// starts, so its baseline is the count from before the run, and it
	// keeps the kills that no tick took yet.
	mem      *host.UnitWatch
	oom      bool // the memory cap stopped a process in the run
	oomAgent bool // it stopped the agent itself (classifyEnd)
	sorted   bool // classifyEnd has decided
	end      *host.LifeEnd
}

// life is the record as the manifest keeps it.
func (r *record) life() *host.Life {
	l := &host.Life{Phase: r.phase, BootBy: r.bootBy, Started: r.started, KillBase: r.mem.Base()}
	if r.end != nil {
		e := *r.end
		l.End = &e
	}
	return l
}

// ended reports whether the record is closed. The caller holds s.mu.
func (s *Supervisor) ended() bool {
	return s.rec.phase == host.LifeEnded || s.rec.phase == host.LifeStopped
}

// setPhase moves the record to phase. The caller holds s.mu.
func (s *Supervisor) setPhase(phase string) {
	if !s.ended() {
		s.rec.phase = phase
		s.dirty = true
	}
}

// classifyEnd reports whether the memory cap stopped the agent itself, at
// the agent's end, whose exit code and signal the guest claims. It decides
// once, by the holder's rule: the kills at the cap that no tick has taken
// yet are the agent's own kill when its signal is SIGKILL, and another
// process's when it is not. A kill that a tick took before is never the
// agent's. The end frame that a client gets and the claimed end on the ctl
// stream both ask, in either order, and get the same answer. Either way,
// the oom alert goes out once: the agent's, as its end, with the exit
// details marked as the guest's, or a child's. The caller holds s.mu.
func (s *Supervisor) classifyEnd(code int, signal string) bool {
	r := &s.rec
	if r.sorted || s.ended() {
		return r.oomAgent
	}
	r.sorted = true
	if kills := r.mem.End(); kills > 0 {
		r.oom = true
		s.audit.write("oom_kill", map[string]any{"kills": kills})
		ev := s.alertEvent(alerts.OOM)
		if isKill(signal) {
			// The agent's end: the kill is the trusted side's fact, and the
			// exit details are the guest's.
			r.oomAgent = true
			ev.ExitCode, ev.Signal, ev.ExitFrom = &code, signal, alerts.FromGuest
			s.end.alerted = true
		}
		s.raise(ev)
	}
	return r.oomAgent
}

// settleKills reads the unit's kill count a last time before the record
// closes, whatever the guest said: a kill that no tick took yet is still
// a fact of the run, with its audit line and its alert. The caller holds
// s.mu.
func (s *Supervisor) settleKills() {
	if kills := s.rec.mem.End(); kills > 0 {
		s.rec.oom = true
		s.audit.write("oom_kill", map[string]any{"kills": kills})
		s.raise(s.alertEvent(alerts.OOM))
	}
}

// closeRecord closes the record with the run's end, of kind and with
// reason, and sets the session's last status from it. The caller holds
// s.mu.
func (s *Supervisor) closeRecord(kind, reason string) {
	if s.ended() {
		return
	}
	s.settleKills()
	r := &s.rec
	r.phase = host.LifeEnded
	if kind == host.EndStop {
		r.phase = host.LifeStopped
	}
	r.end = &host.LifeEnd{Kind: kind, Reason: reason, At: time.Now().UTC(), OOM: r.oom, OOMAgent: r.oomAgent,
		// Existing restore semantics use Claimed for an agent that had
		// naturally ended, not the physical End caused by this stop itself.
		Claimed: s.end.claimed && !s.end.afterStop}
	switch kind {
	case host.EndSandbox:
		s.raiseError()
	case host.EndBoot:
		s.end.alerted = true // the start failed: there is no end to raise
		s.raiseError()
	}
	s.m.Status = s.endStatus()
	if kind==host.EndStop {ev:=s.alertEvent(alerts.SandboxStopped);ev.Reason=reason;s.raise(ev)}
}

// endStatus is the session's last status, from the record. Its
// out-of-memory mark is the agent's own, as a holder's is: a kill of
// another process stays in the record (LifeEnd.OOM). A stop with the host
// shows the guest's last claimed state, marked as claimed: its claimed
// end, which restore honours (host.Manifest.Ended), or a live state, which
// restore starts again. An error of the sandbox says what the guest
// claimed in its reason. The caller holds s.mu.
func (s *Supervisor) endStatus() host.Status {
	e := s.rec.end
	st := host.Status{Since: e.At, Reason: e.Reason, OOM: s.rec.oomAgent}
	switch e.Kind {
	case host.EndEmpty:
		return emptyEndStatus(s)
	case host.EndStop:
		st.State, st.Source = host.StateStopped, "supervisor"
	case host.EndKill:
		st.State = host.StateExited
	case host.EndSystem:
		st.State, st.Source = host.StateWorking, "system"
		switch g := s.m.Guest; {
		case g != nil && g.ExitCode != nil && !s.end.afterStop:
			st = s.claimedStatus()
			st.Since = e.At
		case g != nil && claimStates[g.State]:
			st.State, st.Claimed = g.State, true
		}
	case host.EndBoot:
		code := 127
		st.State, st.ExitCode = host.StateErrored, &code
	default:
		st.State = host.StateErrored
		st.Reason += s.claimNote()
	}
	return st
}
