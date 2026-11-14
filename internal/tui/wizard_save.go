package tui

import (
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

// pendingSave joins a save only after its owned command actually began.
// A dropped or late command can never make program cleanup wait forever.
type pendingSave struct {
	mu               sync.Mutex
	started, stopped bool
	done             chan struct{}
	err              error
}

func (p *pendingSave) begin() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped || p.started {
		return false
	}
	p.started = true
	return true
}

func (p *pendingSave) hasBegun() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.started
}

func (p *pendingSave) finish() {
	p.mu.Lock()
	p.stopped = true
	started := p.started
	p.mu.Unlock()
	if started {
		<-p.done
	}
}

func (w *wizard) save(m *Model) tea.Cmd {
	if !w.tier.Valid() && w.host.Machine != nil {
		w.tier = client.ProviderTier(w.host.Machine.Provider)
	}
	if w.conn != nil && w.conn.HostIDChanged() {
		w.tier = client.TierShared
	}
	w.finishOp()
	w.ctx, w.cancel = w.operationContext(m.backgroundContext())
	w.seq++
	w.run = true
	pending := &pendingSave{done: make(chan struct{})}
	w.saving = pending
	cancel := w.cancel
	m.conns.onClose(func() { cancel(); pending.finish() })
	return m.saveWizardHost(w, w.ctx, w.seq, w.host)
}

func (w *wizard) end(m *Model) {
	w.stop()
	if w.saving != nil {
		w.saving.finish()
	}
	if w.saved || w.made {
		return
	}
	if w.pinScope != nil {
		m.conns.onClose(w.pinScope.close)
		w.staged = false
		return
	}
	if w.host.Name != "" && w.staged {
		m.be.AbortPin(w.host.Name)
		w.staged = false
	}
}
