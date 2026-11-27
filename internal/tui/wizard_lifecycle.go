package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

var wizardSetupContext = func(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 90*time.Second)
}

func (w *wizard) owns(owner *wizard, seq int) bool { return owner == w && seq == w.seq }
func (w *wizard) takeConn() Conn                   { c := w.conn; w.conn = nil; return c }

func (w *wizard) finishOp() {
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	w.ctx = nil
}

func (w *wizard) stop() (inStage bool) {
	w.seq++
	w.run, w.checking, w.starting, w.waitingRoot = false, false, false, false
	if w.tok != nil {
		w.tok.mu.Lock()
		w.tok.stopped = true
		inStage = !w.tok.staged
		w.staged = w.staged || w.tok.staged
		if w.tok.staged {
			w.pinScope = w.tok.scope
		}
		w.tok.mu.Unlock()
		w.tok = nil
	}
	w.finishOp()
	return inStage
}

func (w *wizard) begin(m *Model) (context.Context, int, tea.Cmd) {
	cleanup := m.dropConn(w.takeConn())
	w.stop()
	w.run = true
	w.ctx, w.cancel = w.operationContext(m.backgroundContext())
	return w.ctx, w.seq, cleanup
}

func (w *wizard) beginAgentOp(m *Model) (context.Context, int) {
	w.stop()
	w.ctx, w.cancel = w.operationContext(m.backgroundContext())
	return w.ctx, w.seq
}

// onSetup alone accepts a prepared connection. Every rejected result is
// disposed, even when another wizard has reused the same sequence number.
func (m *Model) onSetup(msg setupMsg) tea.Cmd {
	if w, ok := m.modal.(*wizard); ok && w.owns(msg.owner, msg.seq) && w.step == wzReady && w.run && !w.saved {
		return w.onSetupDone(m, msg)
	}
	return m.dropConn(msg.conn)
}

func (m *Model) resumeWizard() tea.Cmd {
	w, ok := m.modal.(*wizard)
	if !ok || (!w.run && !w.checking && !w.starting && w.conn == nil) {
		return nil
	}
	checking, act, ended := w.checking, w.checkAct, w.checkEnded
	w.stop()
	w.saving = nil
	w.conn = nil
	for i := range w.checks {
		if w.checks[i].state == ckRunning {
			w.checks[i].state = ckFailed
		}
	}
	if checking && w.saved {
		return w.recheck(m, act, ended)
	}
	hint := "Press Enter to try again."
	if w.saved {
		hint = "Check the host's sessions before trying again."
	}
	w.err = &client.UserError{Msg: "the host operation stopped while attached", Hint: hint, Code: "cancelled", Kind: client.KindCancelled}
	return nil
}
