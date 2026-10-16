package host

import (
	"os"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/term"
)

// holderAlerts links a holder to its alert dispatcher (decision 0019), and
// keeps the time the agent worked in the current turn, for turn_done.
type holderAlerts struct {
	d         *alerts.Dispatcher
	agent     string        // the state from the agent's own signals (agentState)
	workSince time.Time     // when the current working spell began
	worked    time.Duration // working time in this turn so far
}

// startAlerts starts the session's alert dispatcher and raises started, or
// restored when restore brought the session back after a host restart.
func (h *Holder) startAlerts(restored bool) {
	home, _ := os.UserHomeDir()
	h.mu.Lock()
	defer h.mu.Unlock()
	// Commands run in the session's login environment, so they find the
	// same programs the user's shell finds.
	h.al.d = alerts.New(alerts.Options{
		Env:    append([]string(nil), h.m.Env...),
		Dir:    home,
		Logger: h.log,
	})
	h.al.agent, h.al.workSince = StateWorking, time.Now() // a session starts out working
	kind := alerts.Started
	if restored {
		kind = alerts.Restored
	}
	h.al.d.Notify(h.alertEvent(kind))
}

// The labels that say why a session's agent starts again (startAgain).
const (
	restoredLabel = "restored_at" // restore, after a host restart
	resumedLabel  = "resumed_at"  // the user's resume
)

// wasRestored reports whether restore started this holder, after a host
// restart. Only then does the session send a restored alert, and start
// without waiting for secrets that nothing can send.
func wasRestored(m *Manifest) bool { return labelSinceStart(m, restoredLabel) }

// wasResumed reports whether the user's resume started this holder. The
// agent then starts again, as after a restore. But the session sends a
// started alert and gets its secrets from the client, as at a new start.
func wasResumed(m *Manifest) bool { return labelSinceStart(m, resumedLabel) }

// labelSinceStart reports whether the time in the label k comes after the
// session's last start: startAgain labels a session after the start before.
func labelSinceStart(m *Manifest, k string) bool {
	at, err := time.Parse(time.RFC3339, m.Labels[k])
	return err == nil && at.After(m.Run.StartedAt)
}

// alertEvent starts an event about this session. The caller holds h.mu.
func (h *Holder) alertEvent(kind string) alerts.Event {
	return alerts.Event{Event: kind, Session: alerts.Session{ID: h.id, Name: h.m.Name, Agent: h.m.Agent.Kind, Dir: h.m.Cwd}}
}

// alertState tells the dispatcher that the agent's own state changed from
// prev to next, and raises needs_input or turn_done. The memory warning does
// not come here: it sends its own alert (alertMemory). The caller holds h.mu.
func (h *Holder) alertState(prev, next string) {
	a := &h.al
	if a.d == nil {
		return
	}
	now := time.Now()
	a.d.StateChanged()
	if prev == StateWorking && !a.workSince.IsZero() {
		a.worked += now.Sub(a.workSince)
		a.workSince = time.Time{}
	}
	switch next {
	case StateWorking:
		a.workSince = now
	case StateNeedsInput:
		ev := h.alertEvent(alerts.NeedsInput)
		ev.Detail = h.question()
		a.d.Notify(ev)
	case StateIdle:
		// The dispatcher drops a turn shorter than min_turn.
		if a.worked > 0 {
			ev := h.alertEvent(alerts.TurnDone)
			ev.Turn = a.worked
			a.d.Notify(ev)
		}
		a.worked = 0
	}
}

// question is the detail of needs_input: the agent's message from its hook,
// or else the bottom of the screen that a rule matched. The dispatcher drops
// it unless the user turned detail on. The caller holds h.mu.
func (h *Holder) question() string {
	if h.notifyMsg != "" {
		return h.notifyMsg
	}
	if h.ruleState != StateNeedsInput || h.scr == nil {
		return ""
	}
	var lines []string
	for _, l := range h.scr.screen().Lines {
		if l = strings.TrimRight(l, " "); strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	s := strings.Join(lines, "\n")
	if len(s) > 1000 {
		s = s[len(s)-1000:]
	}
	return s
}

// alertOOM raises oom: the memory cap stopped a process. The caller holds h.mu.
func (h *Holder) alertOOM() {
	if h.al.d != nil {
		h.al.d.Notify(h.alertEvent(alerts.OOM))
	}
}

// alertEnded raises exited, failed or oom when the program ends by itself.
// Its exit code marks the session's end, which the per-session limit never
// holds back (decision 0026). The caller holds h.mu.
func (h *Holder) alertEnded(ended proto.Ended) {
	if h.al.d == nil {
		return
	}
	kind := alerts.Exited
	switch {
	case ended.OOM:
		kind = alerts.OOM
	case ended.ExitCode != 0 || ended.Signal != "":
		kind = alerts.Failed
	}
	ev := h.alertEvent(kind)
	code := ended.ExitCode
	ev.ExitCode, ev.Signal = &code, ended.Signal
	h.al.d.Notify(ev)
}

// closeAlerts stops the dispatcher when the holder stops the session itself:
// for a host shutdown or a kill, no event goes out. It waits a moment for
// deliveries already under way.
func (h *Holder) closeAlerts() {
	h.mu.Lock()
	d := h.al.d
	h.mu.Unlock()
	if d != nil {
		d.Close(2 * time.Second)
	}
}

// noteTyping tells the dispatcher when input holds keys a person typed, so a
// needs_input in its delay stays quiet.
func noteTyping(d *alerts.Dispatcher, p []byte) {
	if d != nil && personTyped(p) {
		d.Typed()
	}
}

// personTyped reports whether input holds anything but terminal reports, such
// as answers to queries and focus changes, which a terminal sends by itself.
func personTyped(p []byte) bool {
	var ps term.Parser
	typed := false
	ps.Feed(p, func(t term.Token) {
		if !term.IsReport(t) {
			typed = true
		}
	})
	return typed || ps.Pending()
}
