package tui

import "context"

// Close stops current work, retains completed common host changes and closes
// connections. Pin cleanup has a bounded grace; Run joins it after restoration.
func (m *Model) Close() { m.close(m.ctx) }

func (m *Model) close(ctx context.Context) {
	if m.cancelTier != nil {
		m.cancelTier()
	}
	m.pauseBg()
	_ = m.drainHostSaves(ctx, true)
	active := m.modal
	for {
		q, ok := active.(*hostTierQuestion)
		if !ok {
			break
		}
		active = q.previous
	}
	if w, ok := active.(*wizard); ok {
		w.end(m)
		w.conn = nil
	}
	if g, ok := active.(*guide); ok {
		m.stopGuide(g)
	}
	m.stopBundle()
	m.remember()()
	m.conns.closeAll()
	m.conns.finishCleanup()
}

func (m *Model) closeAfterProgram() { m.closeAfterProgramContext(m.ctx) }

// closeAfterProgramContext retains the caller's context for result handling.
// Cleanup and its final join still finish after that context is cancelled.
func (m *Model) closeAfterProgramContext(ctx context.Context) {
	m.close(ctx)
	m.conns.waitCleanup()
}
