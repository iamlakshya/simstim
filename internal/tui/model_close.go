package tui

// Close stops current work, retains completed common host changes and closes
// connections. Pin cleanup has a bounded grace; Run joins it after restoration.
func (m *Model) Close() {
	if m.cancelTier != nil {
		m.cancelTier()
	}
	m.pauseBg()
	_ = m.drainHostSaves(true)
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

func (m *Model) closeAfterProgram() {
	m.Close()
	m.conns.waitCleanup()
}
