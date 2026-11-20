package tui

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

type hostSaveTestBackend struct {
	*fakeBackend
	committed   chan context.Context
	holdCancel  bool
	loadErr     error
	saveErr     error
	loadCalls   atomic.Int32
	saveCalls   atomic.Int32
	connections map[string]*hostSaveTestConn
}

func (b *hostSaveTestBackend) LoadHosts(ctx context.Context) ([]client.Host, error) {
	b.loadCalls.Add(1)
	if b.loadErr != nil {
		return nil, b.loadErr
	}
	return b.fakeBackend.LoadHosts(ctx)
}

func (b *hostSaveTestBackend) SaveHosts(ctx context.Context, hosts []client.Host) error {
	b.saveCalls.Add(1)
	if b.saveErr != nil {
		return b.saveErr
	}
	if err := b.fakeBackend.SaveHosts(ctx, hosts); err != nil {
		return err
	}
	if b.committed != nil {
		b.committed <- ctx
	}
	if b.holdCancel {
		<-ctx.Done()
	}
	return nil // the write already committed, even if cancellation came later
}

func (b *hostSaveTestBackend) Dial(ctx context.Context, host client.Host) (Conn, error) {
	c, err := b.fakeBackend.Dial(ctx, host)
	if err != nil {
		return nil, err
	}
	wrapped := &hostSaveTestConn{fakeConn: c.(*fakeConn)}
	b.connections[host.Name] = wrapped
	return wrapped, nil
}

type hostSaveTestConn struct {
	*fakeConn
	polls  atomic.Int32
	closes atomic.Int32
}

func (c *hostSaveTestConn) Sessions(ctx context.Context) ([]client.Session, error) {
	c.polls.Add(1)
	return c.fakeConn.Sessions(ctx)
}

func (c *hostSaveTestConn) RoundTrip(ctx context.Context) (time.Duration, error) {
	c.polls.Add(1)
	return c.fakeConn.RoundTrip(ctx)
}

func (c *hostSaveTestConn) Doctor(ctx context.Context) (client.HostInfo, error) {
	c.polls.Add(1)
	return c.fakeConn.Doctor(ctx)
}

func (c *hostSaveTestConn) Close() {
	c.closes.Add(1)
	c.fakeConn.Close()
}

func hostSaveFixture(t *testing.T) (*Model, *hostSaveTestBackend, *hostConn) {
	t.Helper()
	be := &hostSaveTestBackend{fakeBackend: newFakeBackend(), connections: map[string]*hostSaveTestConn{}}
	be.hosts = []client.Host{{Name: "forgotten", SSH: "forgotten"}, {Name: "kept", SSH: "kept"}}
	be.state["forgotten"] = &fakeHost{sessions: []client.Session{{ID: "f1", Name: "old", State: "idle", Alive: true}}}
	be.state["kept"] = &fakeHost{sessions: []client.Session{{ID: "k1", Name: "current", State: "idle", Alive: true}}}
	ctx, cancel := context.WithCancel(context.Background())
	m := New(ctx, be, Options{Now: func() time.Time { return now }})
	m.after = func(time.Duration, tea.Msg) tea.Cmd { return nil }
	for _, h := range m.hosts {
		c, err := be.Dial(ctx, h.host)
		if err != nil {
			t.Fatal(err)
		}
		h.conn, h.status, h.loaded = c, stOnline, true
		h.sessions = append([]client.Session(nil), be.state[h.name()].sessions...)
		m.conns.add(c)
	}
	m.syncSel()
	t.Cleanup(func() { cancel(); m.Close() })
	return m, be, m.hostByName("forgotten")
}

func hostSaveReply(t *testing.T, msg tea.Msg) hostSavedMsg {
	t.Helper()
	saved, ok := msg.(hostSavedMsg)
	if !ok {
		t.Fatalf("save result = %#v", msg)
	}
	return saved
}

func hostSaveToastCount(m *Model, text string) int {
	n := 0
	for _, list := range [][]toast{m.toasts.shown, m.toasts.queue} {
		for _, x := range list {
			if strings.Contains(x.text, text) {
				n++
			}
		}
	}
	return n
}

func resumeHostSaveFixture(t *testing.T, m *Model, mode string) *progStepper {
	t.Helper()
	m.openBg(m.ctx)
	ps := newProgStepper(t, m, m)
	var cmd tea.Cmd
	if mode == "attach" {
		cmd = m.resumeAfterAttach(attachDoneMsg{host: "kept", id: "k1", name: "current", res: client.AttachResult{Detached: true}})
	} else {
		cmd = m.resumeAfterExec(editorDoneMsg{})
	}
	ps.step(cmd)
	return ps
}

func TestCommittedCommonHostSaveSurvivesDroppedHandoffReply(t *testing.T) {
	for _, timing := range []string{"commit-before-cancel", "reply-before-cancel"} {
		for _, mode := range []string{"attach", "exec"} {
			for _, state := range []string{"online", "offline"} {
				t.Run(timing+"/"+mode+"/"+state, func(t *testing.T) {
					m, be, h := hostSaveFixture(t)
					old := be.connections[h.name()]
					if state == "offline" {
						h.status = stOffline
					}
					be.committed = make(chan context.Context, 1)
					be.holdCancel = timing == "commit-before-cancel"
					var saved hostSavedMsg
					if be.holdCancel {
						reply := make(chan tea.Msg, 1)
						cmd := m.saveHosts(h.host, true)
						go func() { reply <- cmd() }()
						select {
						case <-be.committed:
						case <-time.After(10 * time.Second):
							t.Fatal("save did not reach the committed return gate")
						}
						m.pauseBg()
						saved = hostSaveReply(t, <-reply)
					} else {
						saved = hostSaveReply(t, m.saveHosts(h.host, true)())
						if ctx := <-be.committed; ctx.Err() != nil {
							t.Fatal("the already-returned reply was not produced before cancellation")
						}
						m.pauseBg()
					}
					if saved.err != nil {
						t.Fatal(saved.err)
					}
					ps := resumeHostSaveFixture(t, m, mode)
					if m.hostByName(h.name()) != nil {
						t.Fatal("the committed forget remained in the resumed launcher")
					}
					if old.polls.Load() != 0 || be.dials[h.name()] != 1 {
						t.Fatalf("forgotten host contacted on resume: polls=%d dials=%d", old.polls.Load(), be.dials[h.name()])
					}
					if !old.isClosed() || old.closes.Load() != 1 {
						t.Fatalf("returned cleanup did not run exactly once: closed=%v calls=%d", old.isClosed(), old.closes.Load())
					}
					if hostSaveToastCount(m, "Forgot forgotten.") != 1 {
						t.Fatal("the committed result was not reported once")
					}
					m.toasts = toastStack{}
					_, late := m.Update(saved)
					ps.step(late)
					ps.step(m.resumeAfterHandoff(func() tea.Cmd { return nil }))
					if hostSaveToastCount(m, "Forgot forgotten.") != 0 {
						t.Fatal("a late reply or repeated drain reported the forget twice")
					}
					m.Close()
					if _, stale := be.Memory().Sessions[h.name()]; stale {
						t.Fatal("Close persisted the forgotten host's cached sessions")
					}
				})
			}
		}
	}
}

func TestCommonHostSaveDrainPrecedesResumeCallbacks(t *testing.T) {
	m, _, h := hostSaveFixture(t)
	_ = hostSaveReply(t, m.saveHosts(h.host, true)())
	m.pauseBg()
	m.openBg(m.ctx)
	ps := newProgStepper(t, m, m)
	cmd := m.resumeAfterHandoff(func() tea.Cmd {
		if m.hostByName(h.name()) != nil {
			t.Error("resumed callback ran before committed host-save reconciliation")
		}
		return nil
	})
	ps.step(cmd)
}

func TestDeliveredCommonHostSaveIsAcknowledged(t *testing.T) {
	m, _, h := hostSaveFixture(t)
	saved := hostSaveReply(t, m.saveHosts(h.host, true)())
	ps := newProgStepper(t, m, m)
	_, cmd := m.Update(saved)
	ps.step(cmd)
	if m.hostByName(h.name()) != nil {
		t.Fatal("normal result delivery did not forget the host")
	}
	m.toasts = toastStack{}
	m.pauseBg()
	ps = resumeHostSaveFixture(t, m, "exec")
	_, cmd = m.Update(saved)
	ps.step(cmd)
	if hostSaveToastCount(m, "Forgot forgotten.") != 0 {
		t.Fatal("an acknowledged result replayed on handoff")
	}
}

func TestCommonHostSaveErrorsReplayOnceWithoutRemoval(t *testing.T) {
	for _, failing := range []string{"load", "save"} {
		t.Run(failing, func(t *testing.T) {
			m, be, h := hostSaveFixture(t)
			failure := errors.New("host save failed")
			if failing == "load" {
				be.loadErr = failure
			} else {
				be.saveErr = failure
			}
			saved := hostSaveReply(t, m.saveHosts(h.host, true)())
			if !errors.Is(saved.err, failure) {
				t.Fatalf("save error = %v", saved.err)
			}
			m.pauseBg()
			ps := resumeHostSaveFixture(t, m, "exec")
			if m.hostByName(h.name()) == nil || hostSaveToastCount(m, "Host save failed.") != 1 {
				t.Fatal("failed save removed its host or lost its error")
			}
			m.toasts = toastStack{}
			_, cmd := m.Update(saved)
			ps.step(cmd)
			ps.step(m.resumeAfterHandoff(func() tea.Cmd { return nil }))
			if hostSaveToastCount(m, "Host save failed.") != 0 {
				t.Fatal("failed save error was reported twice")
			}
		})
	}
}

func TestCloseReconcilesDroppedCommonHostSaveBeforeCache(t *testing.T) {
	m, be, h := hostSaveFixture(t)
	saved := hostSaveReply(t, m.saveHosts(h.host, true)())
	m.Close()
	if m.hostByName(h.name()) != nil {
		t.Fatal("Close did not reconcile the committed host removal")
	}
	if _, stale := be.Memory().Sessions[h.name()]; stale {
		t.Fatal("Close wrote stale forgotten-host sessions")
	}
	if !be.connections[h.name()].isClosed() {
		t.Fatal("Close leaked the forgotten connection")
	}
	m.toasts = toastStack{}
	_, cmd := m.Update(saved)
	if cmd != nil || len(m.toasts.shown)+len(m.toasts.queue) != 0 {
		t.Fatal("a late host-save reply produced a notice after Close")
	}
}

func TestCommonHostSaveCompletionsPreserveOrder(t *testing.T) {
	m, _, first := hostSaveFixture(t)
	second := m.hostByName("kept")
	_ = hostSaveReply(t, m.saveHosts(first.host, true)())
	_ = hostSaveReply(t, m.saveHosts(second.host, true)())
	m.pauseBg()
	m.openBg(m.ctx)
	_ = m.resumeAfterHandoff(func() tea.Cmd { return nil })
	if len(m.hosts) != 0 || len(m.toasts.shown) < 2 {
		t.Fatal("not every completed removal was applied")
	}
	if !strings.HasPrefix(m.toasts.shown[0].text, "Forgot forgotten.") || !strings.HasPrefix(m.toasts.shown[1].text, "Forgot kept.") {
		t.Fatalf("completion order changed: %+v", m.toasts.shown)
	}
}
