package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

// openBg starts a fresh background command context for one tea.Program run.
func (m *Model) openBg(parent context.Context) {
	m.bgMu.Lock()
	if m.bgCancel != nil {
		m.bgCancel()
	}
	m.bgCtx, m.bgCancel = context.WithCancel(parent)
	m.bgMu.Unlock()
}

// pauseBg cancels and joins all active background commands for the current
// tea.Program run and closes any dialled connection that Update never adopted.
func (m *Model) pauseBg() {
	m.bgMu.Lock()
	if m.bgCancel != nil {
		m.bgCancel()
		m.bgCancel = nil
	}
	m.bgMu.Unlock()
	m.bgWG.Wait()
	m.conns.dropUnadopted()
}

// bgCmd runs fn against the current program's background context so an attach
// handoff or Close can cancel and join it cleanly.
func (m *Model) bgCmd(fn func(context.Context) tea.Msg) tea.Cmd {
	return m.bgCmdFrom(m.backgroundContext(), fn)
}

// bgCmdFrom tracks a request whose context also belongs to a modal operation.
func (m *Model) bgCmdFrom(bgCtx context.Context, fn func(context.Context) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		m.bgMu.Lock()
		if bgCtx.Err() != nil {
			m.bgMu.Unlock()
			return nil
		}
		m.bgWG.Add(1)
		m.bgMu.Unlock()
		defer m.bgWG.Done()
		return fn(bgCtx)
	}
}

func (m *Model) backgroundContext() context.Context {
	m.bgMu.Lock()
	bgCtx := m.bgCtx
	m.bgMu.Unlock()
	if bgCtx == nil {
		bgCtx = m.ctx
	}
	return bgCtx
}

func (m *Model) loadSSHHosts() tea.Cmd {
	be := m.be
	return m.bgCmd(func(bgCtx context.Context) tea.Msg {
		hosts := be.SSHConfigHosts()
		if bgCtx.Err() != nil {
			return nil
		}
		return sshHostsMsg{hosts: hosts}
	})
}

func (m *Model) resumeAfterAttach(msg attachDoneMsg) tea.Cmd {
	return m.resumeAfterHandoff(func() tea.Cmd { return m.onAttachEnded(msg) })
}

func (m *Model) resumeAfterExec(msg tea.Msg) tea.Cmd {
	return m.resumeAfterHandoff(func() tea.Cmd { _, cmd := m.Update(msg); return cmd })
}

func (m *Model) resumeAfterHandoff(after func() tea.Cmd) tea.Cmd {
	m.clearHandoff()
	m.ticking = false
	m.preview.inflight = false
	m.rename = nil
	if run := m.dbg.bundling; run != nil {
		run.cancel()
		m.dbg.bundling = nil
	}
	clear(m.resuming)
	for _, h := range m.hosts {
		h.listing, h.pinging, h.doctoring = false, false, false
	}
	completed := m.drainHostSaves(false)
	cmds := []tea.Cmd{
		completed,
		m.resumeWizard(),
		m.resumeGuide(),
		after(),
		m.onDebugTick(),
		m.after(refreshEvery, refreshTickMsg{}),
		m.after(latencyEvery, latencyTickMsg{}),
		m.after(activityEvery, activityTickMsg{}),
	}
	if m.sshHosts == nil {
		cmds = append(cmds, m.loadSSHHosts())
	}
	for _, h := range m.hosts {
		if h.online() {
			cmds = append(cmds, m.waitDone(h.name(), h.gen, h.conn), m.listSessions(h), m.ping(h))
			if h.info == nil {
				cmds = append(cmds, m.doctor(h))
			}
		} else {
			cmds = append(cmds, m.dial(h))
		}
	}
	if s := m.selected(); s != nil && !ended(*s) && m.previewOpen() {
		cmds = append(cmds, m.peek())
	}
	if d, ok := m.toasts.next(m.now()); ok {
		cmds = append(cmds, m.after(d, toastTickMsg{}))
	}
	return tea.Batch(append(cmds, m.startTimers())...)
}
