package host

import (
	"errors"
	"os/exec"
	"syscall"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// oomChildReason is the session's reason after the memory cap stopped a
// process that the agent started, not the agent itself. The client reads
// it too, so it lives in api.
const oomChildReason = api.ReasonChildOOM

// The memory cap's kills come from the oom_kill counter of the session's
// unit (readOOMKills, and each tick's readMem, which reads it before
// the tick takes h.mu). The kernel counts a kill before it sends the SIGKILL
// ("Raise event before sending signal", __oom_kill_process in
// mm/oom_kill.c). So a new count can be the agent's own kill, which a tick
// sees before the agent is reaped. The holder accounts for each kill once:
//
//   - A tick that finds a new count only notes it (oomNoted).
//   - The next tick claims the noted kills for processes that the agent
//     started, if the agent has still not been reaped (checkOOM).
//   - Right after cmd.Wait, reap reads the count once more. A SIGKILL and
//     a count past the kills already claimed (oomSeen) mean the agent's
//     own kill. A count past them without a SIGKILL means other processes'
//     kills. After that, waitLoop owns the session's status.
//
// Two windows remain, each up to two ticks (2 s) long:
//
//   - A SIGKILL that is not out of memory ends the agent before a tick
//     claims a child's kill. The agent's end then counts as out of memory.
//   - The agent takes more than a tick to die after its own kill. The next
//     tick then claims that kill for a child, and the agent's end is a
//     plain SIGKILL.

// checkOOM marks the session when the memory cap stopped a process that the
// agent started, and sends one oom alert for it. kills is the count that
// this tick read. The agent runs on and usually reports the failed tool
// itself, so the session does not start to need input. It does nothing once
// the agent is reaped: reap reads the count again. It returns the kill
// count when a new kill needs a snapshot of the unit's processes, or 0. The
// caller holds h.mu.
func (h *Holder) checkOOM(kills int64) (snap int64) {
	if h.reaped {
		return 0
	}
	noted := h.oomNoted
	h.oomNoted = kills
	snap = h.newSnapshot(kills)
	if noted <= h.oomSeen {
		return snap
	}
	// The agent outlived these kills by a tick: they were other processes'.
	h.oomSeen = noted
	h.m.Status.OOM = true
	// The oom mark and its alert say more than the memory warning.
	h.mem.mark = false
	if h.m.Status.State != StateNeedsInput {
		// While the agent waits, the reason is its question (updateStatus).
		// While the memory warning shows, the state is needs_input too, and
		// the reason stays here. That is right: updateStatus runs next in
		// this tick, finds the mark cleared, and sets the oom reason.
		h.m.Status.Reason = oomChildReason
	}
	h.dirty = true
	h.alertOOM()
	return snap
}

// newSnapshot returns n when the kill count n is past the last snapshot's,
// and accounts for it, or 0. The caller holds h.mu.
func (h *Holder) newSnapshot(n int64) int64 {
	if n <= h.oomSnapped {
		return 0
	}
	h.oomSnapped = n
	return n
}

// reap records that cmd.Wait has returned, so checkOOM stops. It reports
// whether the kill count has grown past the kills that checkOOM claimed,
// and accounts for the new count. It reads the count at once: a process
// that the agent left behind can be stopped later, after the agent's end.
// snap is the kill count when a new kill needs a snapshot, or 0.
func (h *Holder) reap() (newKills bool, snap int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reaped = true
	n := readOOMKills(h.id)
	snap = h.newSnapshot(n)
	if n <= h.oomSeen {
		return false, snap
	}
	h.oomSeen = n
	return true, snap
}

// endOf is how the agent ended, from the error of cmd.Wait, and whether a
// SIGKILL ended it.
func endOf(err error) (ended proto.Ended, killed bool) {
	if err == nil {
		return ended, false
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		ended.ExitCode = 1
		return ended, false
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		ended.Signal = ws.Signal().String()
		ended.ExitCode = 128 + int(ws.Signal())
		return ended, ws.Signal() == syscall.SIGKILL
	}
	ended.ExitCode = ee.ExitCode()
	return ended, false
}
