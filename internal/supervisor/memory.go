package supervisor

import (
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

// The memory facts of a walled session are the supervisor's: it runs in
// the session's unit and reads the unit's memory files, which the guest
// cannot change. The rules are the holder's (holder_mem.go and
// holder_oom.go): the warning marks the session as needing input, with a
// memory alert, and a kill at the cap marks it with the oom reason, with an
// oom alert. The mark goes when the episode ends, or when a person attaches
// after the warning showed, then types.

// unitDir is host.UnitCgroupDir. Tests point it at a fake cgroup folder.
var unitDir = host.UnitCgroupDir

// memMark is the memory warning's mark on the session.
type memMark struct {
	on       bool      // the warning marks the session
	usage    string    // the pinned memory and the cap
	shown    time.Time // when the mark took the session's status
	attached time.Time // the last attach that was no reconnect
	attach   time.Time // the last attach after the mark showed
	typed    time.Time // when a person last typed
	typedAt  time.Time // typed, as the mark's attach saw it
}

// memoryTick reads the unit's memory files once, and marks or clears the
// session.
func (s *Supervisor) memoryTick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended() {
		return
	}
	// The watch runs until the sandbox really ends: a claimed end of the
	// agent does not stop it.
	t := s.rec.mem.Tick()
	at := time.Now()
	switch {
	case t.Kills > 0:
		// A kill at the cap: the oom mark and its alert say more than the
		// warning.
		s.mark.on = false
		s.rec.oom = true
		s.raise(s.alertEvent(alerts.OOM))
		s.audit.write("oom_kill", map[string]any{"kills": t.Kills})
	case t.Warn:
		s.mark = memMark{on: true, usage: t.Usage, attached: s.mark.attached, typed: s.mark.typed}
		ev := s.alertEvent(alerts.Memory)
		ev.Usage = t.Usage
		s.raise(ev)
		s.log.Info("close to the memory cap", "usage", t.Usage)
	case t.Calm:
		s.mark.on = false
	case s.mark.on:
		s.mark.usage = t.Usage
		s.clearMark()
	}
	s.showStatus(at)
}

// clearMark clears the mark when a person attached after the warning
// showed, and then typed. The caller holds s.mu.
func (s *Supervisor) clearMark() {
	w := &s.mark
	if !w.on || s.m.Status.Source != host.SrcMemory {
		return
	}
	if w.attached.After(w.shown) && !w.attached.Equal(w.attach) {
		w.attach, w.typedAt = w.attached, w.typed
		return
	}
	if !w.attach.IsZero() && w.typed.After(w.typedAt) {
		w.on = false
	}
}

// noteAttach records a client's attach: it leads the session's size from
// now on, and an attach that is no reconnect counts for the memory mark.
func (s *Supervisor) noteAttach(c *client, a proto.Attach) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leader = c
	if s.idle.clients == nil {
		s.idle.clients = make(map[*client]struct{})
	}
	s.idle.clients[c] = struct{}{}
	s.m.LastAttachedAt = time.Now().UTC()
	if a.Cols > 0 && a.Rows > 0 {
		s.m.Term.Cols, s.m.Term.Rows = host.CapTerm(a.Cols, a.Rows)
	}
	if !a.Reconnect {
		s.mark.attached = time.Now()
	}
	s.dirty = true
}

// noteInput records keys from a client: it leads the session's size, and
// a person who typed may clear a needs_input in its delay.
func (s *Supervisor) noteInput(c *client, p []byte) {
	s.mu.Lock()
	s.leader = c
	s.mu.Unlock()
	s.typed(p)
}

// typed tells the alerts and the memory mark that a person typed, when p
// holds more than a terminal's own reports.
func (s *Supervisor) typed(p []byte) {
	if !host.PersonTyped(p) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mark.typed = time.Now()
	if s.al != nil {
		s.al.Typed()
	}
}

// noteResize records the size of the leading client's terminal.
func (s *Supervisor) noteResize(c *client, rz proto.Resize) {
	if rz.Cols <= 0 || rz.Rows <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leader == c {
		s.m.Term.Cols, s.m.Term.Rows = host.CapTerm(rz.Cols, rz.Rows)
		s.dirty = true
	}
}

// drop forgets a client that detached or went.
func (s *Supervisor) drop(c *client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.idle.clients, c)
	if s.leader == c {
		s.leader = nil
	}
}
