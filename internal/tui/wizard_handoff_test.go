package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestWizardSetupConnectionDroppedAtAttachHandoff(t *testing.T) {
	m, _, w, _, _ := newHandoffCommandModel(t)
	w.host = client.Host{Name: "new", SSH: "new"}
	msg, ok := w.setup(m)().(setupMsg)
	if !ok || msg.err != nil || msg.conn == nil {
		t.Fatalf("setup = %#v", msg)
	}
	c := unclean(msg.conn).(*handoffConn)
	if c.isClosed() || m.conns.count() != 2 {
		t.Fatal("the setup connection did not reach the result-delivery gate")
	}
	w.stop()
	m.modal = nil // Esc closed the wizard before its result was delivered.
	m.pauseBg()
	if !c.isClosed() || m.conns.count() != 1 {
		t.Fatalf("dropped setup: closed=%v conns=%d", c.isClosed(), m.conns.count())
	}
}

func TestWizardAcceptedConnectionSurvivesAttachHandoff(t *testing.T) {
	m, _, w, _, _ := newHandoffCommandModel(t)
	w.host = client.Host{Name: "new", SSH: "new"}
	msg, ok := w.setup(m)().(setupMsg)
	if !ok || msg.err != nil || msg.conn == nil {
		t.Fatalf("setup = %#v", msg)
	}
	cmd := m.onSetup(msg)
	if cmd == nil {
		t.Fatal("the wizard did not save the prepared host")
	}
	saved, ok := cmd().(hostSavedMsg)
	if !ok || saved.err != nil {
		t.Fatalf("save = %#v", saved)
	}
	_ = m.onHostSaved(saved)
	c := unclean(msg.conn).(*handoffConn)
	m.pauseBg()
	h := m.hostByName("new")
	if c.isClosed() || h == nil || h.conn != msg.conn || unclean(h.conn) != c || m.conns.count() != 2 {
		t.Fatalf("accepted setup: closed=%v host=%+v conns=%d", c.isClosed(), h, m.conns.count())
	}
}

func TestAddHostRejectsDroppedWizardConnection(t *testing.T) {
	m, _, _, be, _ := newHandoffCommandModel(t)
	host := client.Host{Name: "new", SSH: "new"}
	c, err := be.Dial(m.ctx, host)
	if err != nil {
		t.Fatal(err)
	}
	m.conns.addUnadopted(c)
	m.pauseBg()
	m.openBg(m.ctx)
	cmd := m.addHost(host, c, nil)
	h := m.hostByName("new")
	if h == nil || h.conn != nil {
		t.Fatal("a result delivered after handoff adopted a closed connection")
	}
	dialled, ok := cmd().(dialledMsg)
	if !ok || dialled.err != nil || dialled.conn == nil || dialled.conn == c {
		t.Fatalf("replacement dial = %#v", dialled)
	}
	_ = m.onDialled(dialled)
	if !h.online() || h.conn != dialled.conn {
		t.Fatal("the host did not recover with a fresh owned connection")
	}
}

func TestResumeClearsRenameAfterDroppedReply(t *testing.T) {
	m, h, _, _, _ := newHandoffCommandModel(t)
	m.modal = nil
	m.rename = &renameState{key: sessKey{h.name(), "s1"}, old: "job", busy: true}
	m.pauseBg()
	m.openBg(m.ctx)
	_ = m.resumeAfterAttach(attachDoneMsg{host: h.name(), id: "s1", name: "job", res: client.AttachResult{Detached: true}})
	if m.rename != nil {
		t.Fatal("a dropped rename reply left the resumed launcher busy")
	}
	_ = m.onKey(tea.KeyPressMsg{Code: '/'})
	if m.prompt == nil || m.prompt.mode != promptSearch {
		t.Fatal("the resumed launcher did not accept normal keyboard input")
	}
}

func TestResumeClearsInterruptedBundle(t *testing.T) {
	m, h, _, _, _ := newHandoffCommandModel(t)
	cmd := m.debugBundle()
	if m.dbg.bundling == nil {
		t.Fatal("the bundle did not reach its command-delivery gate")
	}
	m.pauseBg()
	m.openBg(m.ctx)
	_ = m.resumeAfterAttach(attachDoneMsg{host: h.name(), id: "s1", name: "job", res: client.AttachResult{Detached: true}})
	if m.dbg.bundling != nil {
		t.Fatal("a dropped bundle result left the resumed launcher busy")
	}
	executeHandoffCommand(cmd)
}
