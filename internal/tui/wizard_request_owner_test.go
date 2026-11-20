package tui

import (
	"context"
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestWizardEscCancelsAgentCheckOwner(t *testing.T) {
	m, h, w, be, _ := newHandoffCommandModel(t)
	w.saved, w.step = true, wzReady
	call := &handoffCall{method: "agents", entered: make(chan context.Context, 1), release: make(chan struct{})}
	defer close(call.release)
	unclean(h.conn).(*handoffConn).call = call
	cmd := w.loadAgents(m)
	reply := make(chan tea.Msg, 1)
	go func() { reply <- cmd() }()
	ctx := <-call.entered
	_ = w.back(m)
	select {
	case <-ctx.Done():
	default:
		t.Fatal("Esc closed the modal without cancelling AgentChecks")
	}
	call.release <- struct{}{}
	_ = m.update(<-reply)
	if m.modal != nil || w.checking || w.starting || len(m.toasts.shown) != 0 || len(be.saved) != 0 {
		t.Fatal("cancelled agent check affected the closed wizard")
	}
}

func TestWizardAgentRequestsGetNewEpochs(t *testing.T) {
	for _, kind := range []string{"agents", "start"} {
		t.Run(kind, func(t *testing.T) {
			m, _, w, be, _ := newHandoffCommandModel(t)
			stale := prepareOwnerWizard(t, m, be, w, kind, true)
			oldCtx := w.ctx
			var command tea.Cmd
			if kind == "agents" {
				command = w.loadAgents(m)
			} else {
				command = w.runAct(m)
			}
			if oldCtx.Err() == nil {
				t.Fatal("new agent request left the old context active")
			}
			before := observeOwnerWizard(w)
			m.handoff = true
			_ = m.update(stale)
			if !reflect.DeepEqual(before, observeOwnerWizard(w)) || m.pendingAttach != nil {
				t.Fatal("old agent reply completed the new request")
			}
			fresh := ownerResult(t, command)
			_ = m.update(fresh)
			if kind == "agents" {
				if w.checking || w.flow == nil {
					t.Fatal("new agent check did not complete")
				}
			} else if msg := fresh.(actStartedMsg); w.starting || w.waiting != msg.s.ID || m.pendingAttach == nil || m.pendingAttach.id != msg.s.ID {
				t.Fatal("new action did not attach its own session")
			}
		})
	}
}

func TestWizardHandoffAllowsRetryAndRefreshesInterruptedChecks(t *testing.T) {
	t.Run("unsaved-setup", func(t *testing.T) {
		m, _, w, _, _ := newHandoffCommandModel(t)
		first := ownerPreparedHost(t, m, w)
		_ = m.onSetup(first)
		w.checks = append(w.checks, check{name: "save", state: ckRunning})
		m.pauseBg()
		m.openBg(m.ctx)
		_ = m.resumeWizard()
		if w.run || w.checking || w.starting || w.conn != nil || w.err == nil || w.checks[len(w.checks)-1].state != ckFailed {
			t.Fatal("handoff left setup wedged or pointing to a closed connection")
		}
		retry, ok := ownerResult(t, w.key(m, tea.KeyPressMsg{Code: tea.KeyEnter})).(setupMsg)
		if !ok || retry.err != nil || retry.conn == nil || retry.conn == first.conn {
			t.Fatalf("interrupted setup cannot retry: %#v", retry)
		}
	})
	t.Run("saved-agent-check", func(t *testing.T) {
		m, _, w, be, _ := newHandoffCommandModel(t)
		w.saved, w.step = true, wzReady
		stale := w.loadAgents(m)
		m.pauseBg()
		m.openBg(m.ctx)
		fresh := ownerResult(t, m.resumeWizard())
		if ownerResult(t, stale) != nil {
			t.Fatal("old deferred check ran after the handoff")
		}
		_ = m.update(fresh)
		if w.checking || w.flow == nil || len(be.callList()) == 0 {
			t.Fatal("interrupted read-only checks were not refreshed")
		}
	})
}

func TestWizardCloseInvalidatesAgentOperations(t *testing.T) {
	m, _, w, _, _ := newHandoffCommandModel(t)
	w.saved, w.step = true, wzDone
	ctx, _ := w.beginAgentOp(m)
	_ = w.agentBack(m)
	if m.modal != nil || ctx.Err() == nil || w.ctx != nil || w.cancel != nil {
		t.Fatal("closing the summary retained its operation owner")
	}
}
