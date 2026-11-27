package supervisor

import (
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/host"
)

// The agent's end, as the guest claims it. Whether the memory cap stopped
// the agent is the record's own fact (classifyEnd in life.go): the kills
// follow the holder's two steps (holder_oom.go and host.UnitWatch), so a
// tick notes a new kill, and the next tick takes it for a process that the
// agent started, unless the agent's end took it first.

// claimEnded records the agent's end that the guest claims. The exit code
// and the signal are claims, and show as claimed. The end raises its
// alert, marked as claimed, but the run goes on until the sandbox ends:
// the memory watch too.
func (s *Supervisor) claimEnded(c claim) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.end.claimed || s.ended() || s.m.Guest == nil {
		return
	}
	s.end.claimed = true
	s.end.afterStop = s.stopIntent
	s.end.token = c.runToken
	code := c.exitCode
	s.m.Guest.ExitCode, s.m.Guest.Signal = &code, c.signal
	agentOOM := s.classifyEnd(code, c.signal)
	s.showStatus(time.Now())
	s.dirty = true
	if s.end.afterStop {
		// Retain physical End for capture/drain proof. It does not create a
		// newly permanent natural exit or a failed alert on intentional stop.
		return
	}
	if agentOOM {
		// classifyEnd raised the agent's oom alert: the cap's kill is the
		// trusted side's fact.
		return
	}
	kind := alerts.Exited
	if code != 0 || c.signal != "" {
		kind = alerts.Failed
	}
	ev := s.alertEvent(kind)
	ev.ExitCode, ev.Signal, ev.ExitFrom, ev.Claimed = &code, c.signal, alerts.FromGuest, true
	s.end.alerted = true
	s.raise(ev)
}

// endOOM reports whether the memory cap stopped the agent, for an end
// frame whose exit code and signal the guest claims, by the record's one
// rule.
func (s *Supervisor) endOOM(code int, signal string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.classifyEnd(code, signal)
}

// turnClock keeps how long the agent worked in its current turn, from the
// guest's status claims, for turn_done.
type turnClock struct {
	since  time.Time     // when the current working spell began, or zero
	worked time.Duration // the earlier spells of this turn
	done   time.Duration // the turn that the last idle ended, for its turn_done
}

// change notes a change of the claimed state from prev to next. Idle ends
// the turn: the guest sends its turn_done after the status.
func (t *turnClock) change(prev, next string, at time.Time) {
	if prev == host.StateWorking && !t.since.IsZero() {
		t.worked += at.Sub(t.since)
		t.since = time.Time{}
	}
	switch next {
	case host.StateWorking:
		t.since = at
	case host.StateIdle:
		t.done, t.worked = t.worked, 0
	}
}

// take returns the time of the turn that ended, or of the turn so far
// when none has, and starts a new turn.
func (t *turnClock) take(at time.Time) time.Duration {
	d := t.done + t.worked
	if !t.since.IsZero() {
		d += at.Sub(t.since)
	}
	t.done, t.worked, t.since = 0, 0, time.Time{}
	return d
}
