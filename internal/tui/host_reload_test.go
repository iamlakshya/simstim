package tui

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestReloadOfAReplacedHostClearsTheOldMachine(t *testing.T) {
	hs := home(t, 80, 24)
	h := hs.m.hostByName("atlas-dev")
	if len(h.sessions) == 0 || h.info == nil || !h.loaded {
		t.Fatal("the old host was not loaded")
	}
	old := unclean(h.conn).(*fakeConn)
	h.backoff, h.rtts, h.rtt = time.Minute, rttWindow{{at: now, d: time.Second}}, rttSpread{n: 12}
	changed := client.Host{Name: h.name(), Machine: &client.Machine{Provider: "lima", ID: "replacement", Created: "2"}}
	replace(hs.be, changed)
	hs.be.state[h.name()].dialErr = errors.New("cannot reach the stopped replacement")
	cmd := hs.m.onHostReloaded(hostReloadedMsg{name: h.name(), host: &changed})
	if len(h.sessions) != 0 || h.info != nil || h.loaded || h.err != nil || h.backoff != 0 || len(h.rtts) != 0 || h.rtt != (rttSpread{}) {
		t.Fatalf("the replacement kept old state: %+v", h)
	}
	hs.do(cmd)
	if !old.isClosed() || len(h.sessions) != 0 || h.info != nil || h.loaded || h.online() {
		t.Errorf("the stopped replacement shows the old machine: %+v, old closed %v", h, old.isClosed())
	}
}

func TestWizardSaveFailureReloadsAndClosesItsConnection(t *testing.T) {
	be := newFakeBackend()
	made := client.Host{Name: "box", SSH: "box", Labels: map[string]string{"saved": "elsewhere"}}
	be.state[made.Name] = be.state["atlas-dev"]
	be.beforeUpdate = func(client.HostChange) { replace(be, made) }
	hs := newHarness(t, be, 80, 24)
	w := newWizard()
	w.host, w.step = client.Host{Name: "box", SSH: "other"}, wzReady
	c := &fakeConn{be: be, name: "box", done: make(chan struct{})}
	hs.m.conns.add(c)
	w.conn, hs.m.modal = c, w
	hs.send(w.save(hs.m)())
	var ue *client.UserError
	if !errors.As(w.err, &ue) || ue.Code != "host_exists" || !c.isClosed() || w.conn != nil {
		t.Fatalf("failed save: err %v, closed %v, conn %v", w.err, c.isClosed(), w.conn)
	}
	if h := hs.m.hostByName("box"); h == nil || !client.SameEntry(h.host, made) {
		t.Fatalf("the launcher did not reload the outside save: %+v", h)
	}
	hs.key("esc", "esc")
	if hs.m.modal != nil {
		t.Errorf("Esc left the failed wizard open: %T", hs.m.modal)
	}
}

func TestUnsavedWizardBackClosesItsConnection(t *testing.T) {
	hs := home(t, 80, 24)
	w := newWizard()
	c := &fakeConn{be: hs.be, name: "atlas-dev", done: make(chan struct{})}
	hs.m.conns.add(c)
	w.conn, hs.m.modal = c, w
	hs.key("esc")
	if w.conn != nil || !c.isClosed() || hs.m.modal != nil {
		t.Errorf("back left a connection: modal %T, conn %v, closed %v", hs.m.modal, w.conn, c.isClosed())
	}
}

func TestCancelledWizardSaveClosesItsConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), saveContextKey{}, "kept"))
	defer cancel()
	be := newFakeBackend()
	m := New(ctx, be, Options{Now: func() time.Time { return now }})
	m.after = func(time.Duration, tea.Msg) tea.Cmd { return nil }
	t.Cleanup(func() { m.closeAfterProgramContext(ctx) })
	w := newWizard()
	w.host, w.step = client.Host{Name: "box", SSH: "box"}, wzReady
	c := &fakeConn{be: be, name: "box", done: make(chan struct{})}
	m.conns.add(c)
	w.conn, m.modal = c, w
	be.saveGate, be.saving = make(chan struct{}), make(chan struct{})
	var once sync.Once
	letGo := func() { once.Do(func() { close(be.saveGate) }) }
	cmd := w.save(m)
	got, done := make(chan tea.Msg, 1), make(chan struct{})
	t.Cleanup(func() { cancel(); letGo(); waitSaveWorker(t, done) })
	go func() { defer close(done); got <- cmd() }()
	select {
	case <-be.saving:
	case <-time.After(10 * time.Second):
		t.Fatal("the owned wizard save did not reach its transaction gate")
	}
	be.mu.Lock()
	saveCtx := be.saveCtx
	be.mu.Unlock()
	if saveCtx == nil || saveCtx.Value(saveContextKey{}) != "kept" {
		t.Fatal("the wizard save lost its construction parent's values")
	}
	cancel()
	letGo()
	var msg tea.Msg
	select {
	case msg = <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("the cancelled wizard save did not end")
	}
	_, cleanup := m.Update(msg)
	executeHandoffCommand(cleanup)
	if w.conn != nil || !c.isClosed() || !errors.Is(w.err, context.Canceled) {
		t.Errorf("cancelled save left a connection: conn %v, closed %v, err %v", w.conn, c.isClosed(), w.err)
	}
	be.mu.Lock()
	defer be.mu.Unlock()
	if len(be.saved) != 0 || len(be.hosts) != 0 {
		t.Fatal("the cancelled wizard saved its host")
	}
}

func TestGuideCreateFailureReloadsTheAttemptedName(t *testing.T) {
	hs := home(t, 80, 24)
	made := client.Host{Name: "box", SSH: "box"}
	replace(hs.be, made)
	hs.be.state[made.Name] = hs.be.state["atlas-dev"]
	g := &guide{seq: 1, checks: []check{{}}, step: gdCreate}
	hs.m.modal = g
	_, scope := g.begin(hs.m)
	hs.send(createdMsg{scope: scope, name: "box", err: &client.UserError{Msg: "already saved", Code: "host_exists"}})
	if h := hs.m.hostByName("box"); h == nil || !client.SameEntry(h.host, made) {
		t.Errorf("failed create did not reload the attempted name: %+v", h)
	}
}
