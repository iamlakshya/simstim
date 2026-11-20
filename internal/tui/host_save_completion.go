package tui

import (
	"slices"
	"sync"

	tea "charm.land/bubbletea/v2"
)

// hostSaveCompletion keeps one immutable common-save result until Update
// acknowledges it. The token itself is never written to disk or the wire.
type hostSaveCompletion struct {
	msg hostSavedMsg
}

type hostSaveCompletions struct {
	mu      sync.Mutex
	pending []*hostSaveCompletion
	closed  bool
}

// completeHostSave publishes before the command returns, even if its context
// is still live: the old program can drop a result after it was produced.
func (m *Model) completeHostSave(msg hostSavedMsg) hostSavedMsg {
	if msg.owner != nil {
		return msg // wizard results retain their own owner and stage guards
	}
	c := &hostSaveCompletion{msg: msg}
	m.hostSaves.mu.Lock()
	if !m.hostSaves.closed {
		m.hostSaves.pending = append(m.hostSaves.pending, c)
	}
	m.hostSaves.mu.Unlock()
	msg.completion = c
	return msg
}

func (s *hostSaveCompletions) acknowledge(msg hostSavedMsg) (hostSavedMsg, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return hostSavedMsg{}, false
	}
	if msg.completion == nil {
		return msg, true
	}
	for i, c := range s.pending {
		if c == msg.completion {
			s.pending = slices.Delete(s.pending, i, i+1)
			return c.msg, true
		}
	}
	return hostSavedMsg{}, false // already acknowledged or owned by another Model
}

func (s *hostSaveCompletions) snapshot() []hostSavedMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs := make([]hostSavedMsg, len(s.pending))
	for i, c := range s.pending {
		msgs[i] = c.msg
		msgs[i].completion = c
	}
	return msgs
}

func (s *hostSaveCompletions) close() {
	s.mu.Lock()
	s.closed = true
	s.pending = nil
	s.mu.Unlock()
}

// drainHostSaves runs after pauseBg joins, before any resumed polling, host
// reload or Close's cache save. The ordinary acknowledgement and common-result
// handler produce cleanup commands and notices exactly once.
func (m *Model) drainHostSaves(closing bool) tea.Cmd {
	msgs := m.hostSaves.snapshot()
	cmds := make([]tea.Cmd, 0, len(msgs))
	for _, msg := range msgs {
		if saved, ok := m.hostSaves.acknowledge(msg); ok {
			cmds = append(cmds, m.applyHostSaved(saved))
		}
	}
	if closing {
		m.hostSaves.close()
	}
	return tea.Batch(cmds...)
}
