package host

import (
	"context"
	"github.com/iamlakshya/simstim/internal/phaseclock"
	"time"
)

// Life is the trusted record of a walled session's run: its phase, the
// deadline of its boot, and how it ended (isolation spec, 10.7). Only the
// session's supervisor writes it, from its own facts. The guest's words
// live in Guest and never reach it. Every reader of a walled session's run
// derives from it: the row's last state, restore, and the wait of New and
// resume for the start.
type Life struct {
	Phase string `json:"phase"`
	// BootBy is the deadline of the sandbox's boot.
	BootBy time.Time `json:"boot_by,omitzero"`
	// Started is when the boot ended in a run. It stays zero when the boot
	// failed, or a stop came first.
	Started time.Time `json:"started,omitzero"`
	// KillBase is the memory cap's kill count of the session's unit before
	// the guest started: every kill above it belongs to this run.
	KillBase int64    `json:"kill_base,omitempty"`
	End      *LifeEnd `json:"end,omitempty"`
}

// The phases of a walled session's run, in order.
const (
	LifeBooting = "booting"
	LifeRunning = "running"
	LifeEnding  = "ending"
	LifeEnded   = "ended"
	LifeStopped = "stopped"
)

// LifeEnd is how a walled session's run ended, from the trusted side.
type LifeEnd struct {
	Kind   string    `json:"kind"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
	// OOM is true when the memory cap stopped a process in the run, as the
	// unit's own count says. OOMAgent is true when it stopped the agent
	// itself: a kill that no tick took, at the agent's claimed SIGKILL.
	OOM      bool `json:"oom,omitempty"`
	OOMAgent bool `json:"oom_agent,omitempty"`
	// Claimed is true when the guest claimed a natural agent end before the
	// controller's Stop/System intent. A physical End caused by that stop
	// remains in GuestClaims and the live drain/capture correlation instead.
	// restore leaves a session that a stop with the host found so, as it
	// leaves one whose agent ended without a wall. A forged end gives a
	// guest nothing that its own exit would not.
	Claimed bool `json:"claimed,omitempty"`
}

// The kinds of a walled run's end.
const (
	EndStop    = "stop"    // an explicit trusted stop after actual sandbox death
	EndBoot    = "boot"    // the boot failed
	EndKill    = "kill"    // a kill by request, as for rm and resume
	EndSystem  = "system"  // a stop with the host, also during the boot
	EndSandbox = "sandbox" // the sandbox ended by itself, or its link did
)

// Ended reports whether the session's agent has ended for good, so that
// restore leaves it and its row keeps its last state. For a walled
// session, only its supervisor's record says so: an end other than a stop
// with the host, or a stop with the host after the guest claimed the
// agent's end. A supervisor that stopped without its record of the end
// leaves the session not ended, whatever the guest claimed. For another
// session, the holder's record of the agent's exit says so.
func (m *Manifest) Ended() bool {
	if m.Wall != nil {
		return m.Life != nil && m.Life.End != nil && (m.Life.End.Kind != EndSystem || m.Life.End.Claimed)
	}
	return m.Status.State == StateExited || m.Status.State == StateErrored
}

// bootSlack is how long New and resume wait past the boot's deadline, for
// the supervisor's last save.
const bootSlack = 5 * time.Second

// waitBoot waits until the supervisor of walled session id records the end
// of its sandbox's boot: a run, or a failure, which it returns as an error
// with the supervisor's reason. A claim of the guest never counts, such as
// an agent that exits at once with a non-zero code. It waits until the
// boot's deadline, and bootSlack more. A supervisor that stops before it
// records the end fails the wait at once.
func waitBoot(id string) (*Manifest, error) {
	const hint = "Check the session log with the host doctor."
	deadline := phaseclock.Current.Now().Add(bootSlack) // the supervisor writes its record before its socket answers
	next := phaseclock.Current.Now()
	for {
		m, err := LoadManifest(id)
		if err == nil && m.Life != nil {
			l := m.Life
			switch {
			case !l.Started.IsZero():
				return m, nil
			case l.End != nil:
				return m, UserError("start_failed", l.End.Reason, hint)
			}
			deadline = l.BootBy.Add(bootSlack)
		}
		now := phaseclock.Current.Now()
		if now.After(deadline) {
			return m, UserError("start_failed", "the session's sandbox did not start in time", hint)
		}
		if now.After(next) {
			// Dialing is dearer than reading: four times a second.
			if !listens(id) {
				if m, err := LoadManifest(id); err == nil && m.Life != nil && (m.Life.End != nil || !m.Life.Started.IsZero()) {
					continue // the last save came after the last read
				}
				return m, UserError("start_failed", "the session's supervisor stopped while its sandbox started", hint)
			}
			next = now.Add(250 * time.Millisecond)
		}
		_ = phaseclock.Sleep(context.Background(), 20*time.Millisecond)
	}
}
