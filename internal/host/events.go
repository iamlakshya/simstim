package host

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
)

const (
	eventsLogMaxBytes   = 2 << 20 // 2 MiB before rotating to events.jsonl.1
	defaultEventsLimit  = 100
	maxEventsReadBuffer = 256 << 10
)

var eventsMemMu sync.Mutex

// RecordWatchEvent appends one session or sandbox event to the host's event
// stream (paths.EventsLog) so `events.watch` followers receive it immediately
// without polling (isolation spec 17.6). Errors are ignored so event recording
// never slows or disrupts a running session.
func RecordWatchEvent(ev api.WatchEvent) {
	ev.V = 1
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	} else {
		ev.Time = ev.Time.UTC()
	}
	if ev.Host == "" {
		ev.Host, _ = os.Hostname()
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	b = append(b, '\n')

	logPath := paths.EventsLog()
	if err := paths.EnsurePrivateDir(filepath.Dir(logPath)); err != nil {
		return
	}
	eventsMemMu.Lock()
	defer eventsMemMu.Unlock()
	unlock := lockEventsFile(paths.EventsLock())
	defer unlock()

	if st, err := os.Stat(logPath); err == nil && st.Size()+int64(len(b)) > eventsLogMaxBytes {
		_ = os.Rename(logPath, logPath+".1")
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(b)
	_ = f.Close()
}

// ReadWatchEvents returns up to limit recent events from the host's event
// stream, filtered to sessionID when non-empty.
func ReadWatchEvents(sessionID string, limit int) ([]api.WatchEvent, error) {
	if limit <= 0 {
		limit = defaultEventsLimit
	}
	logPath := paths.EventsLog()
	var all []api.WatchEvent
	for _, p := range []string{logPath + ".1", logPath} {
		evs := readEventsFile(p, sessionID)
		all = append(all, evs...)
	}
	if len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all, nil
}

func readEventsFile(path, sessionID string) []api.WatchEvent {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []api.WatchEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 8<<10), maxEventsReadBuffer)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev api.WatchEvent
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if sessionID != "" && ev.Session != sessionID {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// recordHolderStateEvent records a state_changed event for an unwalled holder.
func (h *Holder) recordHolderStateEvent(prev, next, reason string) {
	if h.isGuest() || prev == next {
		return
	}
	RecordWatchEvent(api.WatchEvent{
		Event:   api.EventStateChanged,
		Session: h.id,
		Name:    h.m.Name,
		Agent:   h.m.Agent.Kind,
		State:   next,
		Prev:    prev,
		Reason:  reason,
	})
}

// recordHolderAlertEvent records needs_input, turn_done or ended for an
// unwalled holder.
func (h *Holder) recordHolderAlertEvent(kind, detail string, code *int, sig string) {
	if h.isGuest() {
		return
	}
	RecordWatchEvent(api.WatchEvent{
		Event:    kind,
		Session:  h.id,
		Name:     h.m.Name,
		Agent:    h.m.Agent.Kind,
		Detail:   detail,
		ExitCode: code,
		Signal:   sig,
	})
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
