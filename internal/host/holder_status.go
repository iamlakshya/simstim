package host

import (
	"github.com/iamlakshya/simstim/internal/agents"
	"time"
)

// updateStatus sets the session's state from the screen rules, the agent's
// hooks and notifications, the memory warning, and its output. The caller
// holds h.mu.
func (h *Holder) updateStatus() {
	if h.scr != nil && h.scr.dirty {
		h.scr.dirty = false
		h.ruleState, h.ruleID, _ = agents.Detect(h.m.Agent.Kind, h.scr.screen())
		if full := h.scr.altScreen(); full != h.m.Status.FullScreen {
			h.m.Status.FullScreen = full
			h.dirty = true
		}
	}
	state, src := h.agentState()
	agentPrev := h.al.agent
	h.al.agent = state
	if h.mem.mark && state != StateNeedsInput {
		// Close to the memory cap (holder_mem.go), after the agent's own wait.
		state, src = StateNeedsInput, srcMemory
	}
	// The wait can also pass between the agent and the memory warning.
	handover := state == StateNeedsInput && (src == srcMemory) != (h.m.Status.Source == srcMemory)
	if state != h.m.Status.State || handover {
		oom := h.m.Status.OOM
		h.m.Status = Status{State: state, Since: time.Now().UTC(), Source: src, FullScreen: h.m.Status.FullScreen, OOM: oom}
		switch {
		case src == srcMemory:
			h.m.Status.Reason = h.mem.reason()
			h.mem.shown = time.Now()
		case state == StateNeedsInput:
			// The agent's question, if its hook gave one.
			h.m.Status.Reason = h.notifyMsg
		case oom:
			// The memory cap stopped a process that the agent started.
			h.m.Status.Reason = oomChildReason
		}
		h.dirty = true
	}
	if h.al.agent != agentPrev {
		// Alerts follow the agent's own state, so its turns and questions
		// count while the memory warning shows too.
		h.alertState(agentPrev, h.al.agent)
	}
}

// agentState is the session's state from the agent's own signals, without
// the memory warning. The caller holds h.mu.
func (h *Holder) agentState() (state, src string) {
	state, src = StateIdle, "quiet"
	switch {
	case h.ruleState == StateNeedsInput:
		state, src = StateNeedsInput, "screen:"+h.ruleID
	case h.needInput:
		state, src = StateNeedsInput, "notify"
	case h.ruleState == StateWorking:
		state, src = StateWorking, "screen:"+h.ruleID
	case h.hookBusy:
		state, src = StateWorking, "hook"
	case h.busy:
		state, src = StateWorking, "progress"
	case h.hookSeen && !h.lastTurnEnd.IsZero() && time.Since(h.lastTurnEnd) < 3*time.Second:
		state, src = StateIdle, "hook"
	case time.Since(h.lastOut) < 3*time.Second:
		state, src = StateWorking, "output"
	}
	return state, src
}
