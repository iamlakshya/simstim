package tui

import (
	"context"
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

func ownerResult(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var result tea.Msg
		for _, child := range batch {
			if next := ownerResult(t, child); next != nil {
				if result != nil {
					t.Fatal("more than one operation result")
				}
				result = next
			}
		}
		return result
	}
	return msg
}

func ownerOnlineHost(t *testing.T, m *Model, be *handoffBackend, h client.Host) {
	t.Helper()
	if m.hostByName(h.Name) != nil {
		return
	}
	c, err := be.Dial(m.ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	if !m.conns.add(c) {
		t.Fatal("could not track fixture connection")
	}
	m.hosts = append(m.hosts, &hostConn{host: h, conn: c, status: stOnline})
}

func prepareOwnerWizard(t *testing.T, m *Model, be *handoffBackend, w *wizard, kind string, run bool) tea.Msg {
	t.Helper()
	m.modal = w
	var cmd tea.Cmd
	switch kind {
	case "probe":
		cmd = w.probe(m)
	case "install":
		cmd = w.install(m)
	case "setup", "save":
		cmd = w.setup(m)
		if kind == "save" {
			msg, ok := ownerResult(t, cmd).(setupMsg)
			if !ok || msg.err != nil {
				t.Fatalf("prepare setup=%#v", msg)
			}
			cmd = m.onSetup(msg)
		}
	case "agents", "start":
		ownerOnlineHost(t, m, be, w.host)
		w.saved = true
		w.step = wzReady
		if kind == "agents" {
			cmd = w.loadAgents(m)
		} else {
			w.step = wzRun
			w.act = onboard.Action{Args: client.NewArgs{Agent: "shell", Name: "install"}}
			cmd = w.runAct(m)
		}
	default:
		t.Fatalf("unknown wizard operation %q", kind)
	}
	if run {
		return ownerResult(t, cmd)
	}
	return nil
}

type ownerWizardState struct {
	step                           wizStep
	seq                            int
	host                           client.Host
	run, checking, starting, saved bool
	contextLive                    bool
	waiting                        string
	conn                           Conn
	checks                         []check
	info                           *client.HostInfo
	plat                           client.Platform
	flow                           *onboard.Flow
	err                            error
}

func observeOwnerWizard(w *wizard) ownerWizardState {
	return ownerWizardState{step: w.step, seq: w.seq, host: w.host, run: w.run, checking: w.checking, starting: w.starting, saved: w.saved,
		contextLive: w.ctx != nil && w.ctx.Err() == nil, waiting: w.waiting, conn: w.conn, checks: append([]check(nil), w.checks...), info: w.info, plat: w.plat, flow: w.flow, err: w.err}
}

func TestWizardRepliesBelongToTheirModal(t *testing.T) {
	for _, kind := range []string{"probe", "install", "setup", "save", "agents", "start", "same-host-save"} {
		t.Run(kind, func(t *testing.T) {
			m, _, first, be, _ := newHandoffCommandModel(t)
			first.host = client.Host{Name: "first", SSH: "first"}
			second := newWizard()
			second.host = client.Host{Name: "second", SSH: "second"}
			if kind == "same-host-save" {
				first.host, second.host = client.Host{Name: "same", SSH: "same"}, client.Host{Name: "same", SSH: "same"}
				kind = "save"
			}
			msg := prepareOwnerWizard(t, m, be, first, kind, true)
			if msg == nil {
				t.Fatal("first operation produced no reply")
			}
			// Stop the old controller without delivering its queued reply.
			first.stop()
			executeHandoffCommand(m.dropConn(first.takeConn()))
			prepareOwnerWizard(t, m, be, second, kind, false)
			var messageSeq int
			switch got := msg.(type) {
			case probeMsg:
				messageSeq = got.seq
			case installMsg:
				messageSeq = got.seq
			case setupMsg:
				messageSeq = got.seq
			case hostSavedMsg:
				messageSeq = got.seq
			case agentChecksMsg:
				messageSeq = got.seq
			case actStartedMsg:
				messageSeq = got.seq
			}
			if second.seq != messageSeq {
				t.Fatalf("test missed equal-sequence collision: current=%d reply=%d", second.seq, messageSeq)
			}
			before := observeOwnerWizard(second)
			count, saved, toasts := m.conns.count(), len(be.saved), len(m.toasts.shown)
			m.handoff = true
			cleanup := m.update(msg)
			if after := observeOwnerWizard(second); !reflect.DeepEqual(before, after) {
				t.Fatalf("old %s reply changed current modal:\nbefore=%+v\nafter=%+v", kind, before, after)
			}
			executeHandoffCommand(cleanup)
			if len(be.saved) != saved || len(m.toasts.shown) != toasts || m.pendingAttach != nil {
				t.Fatal("old reply saved, attached or reported an error in the current wizard")
			}
			if setup, ok := msg.(setupMsg); ok {
				if !unclean(setup.conn).(*handoffConn).isClosed() || m.conns.count() != count-1 {
					t.Fatal("rejected setup reply kept its unadopted connection")
				}
			} else if m.conns.count() != count {
				t.Fatal("rejected reply dropped the current wizard's connection")
			}
		})
	}
}

func TestWizardRepliesRequireCurrentRequestAndStage(t *testing.T) {
	for _, stale := range []string{"request", "stage", "inactive", "saved"} {
		for _, kind := range []string{"probe", "install", "setup", "save", "agents", "start"} {
			if stale == "saved" && kind != "setup" && kind != "save" && kind != "agents" {
				continue
			}
			t.Run(stale+"/"+kind, func(t *testing.T) {
				m, _, w, be, _ := newHandoffCommandModel(t)
				w.host = client.Host{Name: "new", SSH: "new"}
				msg := prepareOwnerWizard(t, m, be, w, kind, true)
				switch stale {
				case "request":
					w.seq++
				case "stage":
					w.step = wzDone
				case "inactive":
					switch kind {
					case "agents":
						w.checking = false
					case "start":
						w.starting = false
					default:
						w.run = false
					}
				case "saved":
					w.saved = kind != "agents"
				}
				before := observeOwnerWizard(w)
				m.handoff = true
				cleanup := m.update(msg)
				if after := observeOwnerWizard(w); !reflect.DeepEqual(before, after) {
					t.Fatalf("stale %s changed modal:\nbefore=%+v\nafter=%+v", kind, before, after)
				}
				executeHandoffCommand(cleanup)
				if m.pendingAttach != nil {
					t.Fatal("stale action attached")
				}
				if setup, ok := msg.(setupMsg); ok && !unclean(setup.conn).(*handoffConn).isClosed() {
					t.Fatal("stale setup connection stayed open")
				}
			})
		}
	}
}

func TestGenericAndWizardSaveRepliesStaySeparate(t *testing.T) {
	m, _, w, _, _ := newHandoffCommandModel(t)
	w.step, w.run = wzReady, true
	w.ctx, w.cancel = context.WithCancel(m.ctx)
	before := observeOwnerWizard(w)
	err := &client.UserError{Msg: "generic save failed", Hint: "Try again.", Code: "save_failed"}
	_ = m.onHostSaved(hostSavedMsg{host: w.host, err: err})
	if !reflect.DeepEqual(before, observeOwnerWizard(w)) || len(m.toasts.shown) != 1 {
		t.Fatal("generic save was swallowed by an unrelated wizard")
	}
	first := newWizard()
	for _, modal := range []modal{nil, w} {
		m.modal = modal
		before := len(m.toasts.shown)
		_ = m.onHostSaved(hostSavedMsg{owner: first, seq: first.seq, host: first.host, err: context.Canceled})
		if len(m.toasts.shown) != before {
			t.Fatal("discarded wizard save produced a phantom error")
		}
	}
}
