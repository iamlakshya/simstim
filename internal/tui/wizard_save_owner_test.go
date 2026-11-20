package tui

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

type ownerSaveBackend struct {
	Backend
	load         func(context.Context) ([]client.Host, error)
	save         func(context.Context, []client.Host) error
	loads, saves atomic.Int32
}

func (b *ownerSaveBackend) LoadHosts(ctx context.Context) ([]client.Host, error) {
	b.loads.Add(1)
	if b.load != nil {
		return b.load(ctx)
	}
	return b.Backend.LoadHosts(ctx)
}

func (b *ownerSaveBackend) SaveHosts(ctx context.Context, hosts []client.Host) error {
	b.saves.Add(1)
	if b.save != nil {
		return b.save(ctx, hosts)
	}
	return b.Backend.SaveHosts(ctx, hosts)
}

func ownerPreparedHost(t *testing.T, m *Model, w *wizard) setupMsg {
	t.Helper()
	w.host = client.Host{Name: "new", SSH: "new"}
	msg, ok := ownerResult(t, w.setup(m)).(setupMsg)
	if !ok || msg.err != nil || msg.conn == nil {
		t.Fatalf("setup=%#v", msg)
	}
	return msg
}

func TestWizardEscDisposesConnectionAndPreventsLateSave(t *testing.T) {
	m, _, w, be, _ := newHandoffCommandModel(t)
	setup := ownerPreparedHost(t, m, w)
	entered, release := make(chan context.Context, 1), make(chan struct{})
	defer close(release)
	backend := &ownerSaveBackend{Backend: be}
	backend.load = func(ctx context.Context) ([]client.Host, error) {
		entered <- ctx
		<-release
		// Deliberately return success after cancellation. The wizard must
		// check its owner context before issuing a save to any backend.
		return []client.Host{{Name: "box", SSH: "box"}}, nil
	}
	m.be = backend
	cmd := m.onSetup(setup)
	reply := make(chan tea.Msg, 1)
	go func() { reply <- cmd() }()
	ctx := <-entered
	if ctx.Value(handoffContextKey{}) != "kept" {
		t.Fatal("wizard save lost context values")
	}
	executeHandoffCommand(w.back(m))
	select {
	case <-ctx.Done():
	default:
		t.Fatal("Esc did not cancel the actual save context")
	}
	if w.conn != nil || !unclean(setup.conn).(*handoffConn).isClosed() || m.conns.count() != 1 {
		t.Fatal("Esc kept the unsaved wizard connection")
	}
	// Release without double-closing the deferred cleanup channel.
	release <- struct{}{}
	msg := <-reply
	if backend.saves.Load() != 0 {
		t.Fatal("cancelled wizard issued SaveHosts after LoadHosts returned")
	}
	before := len(m.toasts.shown)
	_ = m.onHostSaved(msg.(hostSavedMsg))
	if len(m.toasts.shown) != before {
		t.Fatal("discarded save reported an error after Esc")
	}
}

func TestWizardDeferredSaveDoesNothingAfterEsc(t *testing.T) {
	m, _, w, be, _ := newHandoffCommandModel(t)
	setup := ownerPreparedHost(t, m, w)
	backend := &ownerSaveBackend{Backend: be}
	m.be = backend
	cmd := m.onSetup(setup)
	executeHandoffCommand(w.back(m))
	if result := ownerResult(t, cmd); result != nil || backend.loads.Load() != 0 || backend.saves.Load() != 0 {
		t.Fatalf("deferred cancelled save ran: result=%#v loads=%d saves=%d", result, backend.loads.Load(), backend.saves.Load())
	}
	if w.conn != nil || !unclean(setup.conn).(*handoffConn).isClosed() {
		t.Fatal("deferred cancelled save kept the prepared connection")
	}
}

func TestWizardFailedSaveDropsConnectionBeforeRetry(t *testing.T) {
	m, _, w, be, _ := newHandoffCommandModel(t)
	first := ownerPreparedHost(t, m, w)
	failure := errors.New("saved-host write failed")
	backend := &ownerSaveBackend{Backend: be}
	backend.save = func(ctx context.Context, hosts []client.Host) error {
		if backend.saves.Load() == 1 {
			return failure
		}
		return be.SaveHosts(ctx, hosts)
	}
	m.be = backend
	msg := ownerResult(t, m.onSetup(first)).(hostSavedMsg)
	executeHandoffCommand(m.onHostSaved(msg))
	if !errors.Is(w.err, failure) || w.run || w.conn != nil || !unclean(first.conn).(*handoffConn).isClosed() || m.conns.count() != 1 {
		t.Fatal("save failure did not release its connection and enable retry")
	}
	retry := ownerResult(t, w.key(m, tea.KeyPressMsg{Code: tea.KeyEnter})).(setupMsg)
	if retry.err != nil || retry.conn == nil || retry.conn == first.conn {
		t.Fatalf("retry reused failed connection: %#v", retry)
	}
	saved := ownerResult(t, m.onSetup(retry)).(hostSavedMsg)
	_ = m.onHostSaved(saved)
	h := m.hostByName("new")
	if saved.err != nil || !w.saved || w.conn != nil || h == nil || h.conn != retry.conn || m.conns.count() != 2 {
		t.Fatalf("retry did not adopt one new connection: save=%#v host=%+v", saved, h)
	}
}

func TestWizardBeginDisposesLeftoverSetupConnection(t *testing.T) {
	m, _, w, _, _ := newHandoffCommandModel(t)
	first := ownerPreparedHost(t, m, w)
	_ = m.onSetup(first) // prepared connection awaits a save that never ran
	second := ownerResult(t, w.setup(m)).(setupMsg)
	if !unclean(first.conn).(*handoffConn).isClosed() || second.conn == first.conn || second.err != nil || m.conns.count() != 2 {
		t.Fatal("new step leaked the previous prepared connection")
	}
}
