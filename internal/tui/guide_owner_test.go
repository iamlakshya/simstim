package tui

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

type ownerCreateLocal struct {
	onboard.Local
	started chan struct{}
}

func (l ownerCreateLocal) Create(ctx context.Context, kind, name string, out io.Writer) (client.Host, error) {
	close(l.started)
	return l.Local.Create(ctx, kind, name, out)
}

type ownerCreateBackend struct {
	Backend
	local onboard.Local
}

func (b ownerCreateBackend) Local() onboard.Local { return b.local }

func ownerGuide(t *testing.T, m *Model) *guide {
	t.Helper()
	cmd := m.openGuide()
	g := m.modal.(*guide)
	_ = m.update(ownerResult(t, cmd))
	return g
}

func ownerChoose(t *testing.T, m *Model, g *guide, kind string) {
	t.Helper()
	p, ok := onboard.FindPlace(g.places, kind)
	if !ok || !p.Ready {
		t.Fatalf("fake place unavailable: %s %+v", kind, p)
	}
	_ = g.choose(m, p)
}

func closeOwnerGuide(t *testing.T, m *Model, g *guide) {
	t.Helper()
	for i := 0; i < 8 && m.modal == g; i++ {
		_ = g.back(m)
	}
	if m.modal != nil {
		t.Fatalf("guide did not close: step=%d run=%v", g.step, g.run)
	}
}

func TestGuideFactsBelongToTheirModal(t *testing.T) {
	t.Run("places", func(t *testing.T) {
		m, _, _, be, _ := newHandoffCommandModel(t)
		local := be.fakeBackend.Local().(*fakeLocal)
		local.has["limactl"] = false
		old := ownerGuide(t, m)
		lima, _ := onboard.FindPlace(old.places, onboard.Lima)
		stale := old.choose(m, lima)
		closeOwnerGuide(t, m, old)
		local.has["limactl"] = true
		current := ownerGuide(t, m)
		fresh := current.probe(m, "")
		before := current.scope()
		_ = m.update(ownerResult(t, stale))
		if m.modal != current || current.scope() != before || current.kind != "" || current.step != gdWhere {
			t.Fatal("old places reply chose a provider for a later guide")
		}
		_ = m.update(ownerResult(t, fresh))
		if p, _ := onboard.FindPlace(current.places, onboard.Lima); !p.Ready || current.step != gdWhere {
			t.Fatal("current places reply did not refresh availability")
		}
	})
	t.Run("public-key", func(t *testing.T) {
		m, _, _, be, _ := newHandoffCommandModel(t)
		local := be.fakeBackend.Local().(*fakeLocal)
		old := ownerGuide(t, m)
		old.step, local.key = gdRentKey, "first-key"
		stale := ownerResult(t, old.findKey(m))
		closeOwnerGuide(t, m, old)
		current := ownerGuide(t, m)
		current.step, local.key = gdRentKey, "second-key"
		fresh := current.findKey(m)
		_ = m.update(stale)
		if current.plan.Key != "" || current.step != gdRentKey {
			t.Fatal("old public key changed a later guide")
		}
		_ = m.update(ownerResult(t, fresh))
		if current.plan.Key != "second-key" {
			t.Fatal("current public key was not accepted")
		}
	})
	t.Run("saved-name", func(t *testing.T) {
		m, _, _, be, _ := newHandoffCommandModel(t)
		local := be.fakeBackend.Local().(*fakeLocal)
		local.sshHosts = []string{"first"}
		old := ownerGuide(t, m)
		old.step, old.in = gdRentName, newInput("first", "")
		stale := ownerResult(t, old.nameServer(m))
		closeOwnerGuide(t, m, old)
		current := ownerGuide(t, m)
		current.step, current.in = gdRentName, newInput("second", "")
		fresh := current.nameServer(m)
		_ = m.update(stale)
		if current.step != gdRentName || current.present || current.plan.Name != "second" {
			t.Fatal("old saved-name check skipped the current check")
		}
		_ = m.update(ownerResult(t, fresh))
		if current.step != gdRentConfig || current.present {
			t.Fatal("current name lookup was not accepted")
		}
	})
	t.Run("ssh-check", func(t *testing.T) {
		m, _, _, _, _ := newHandoffCommandModel(t)
		old := ownerGuide(t, m)
		old.step, old.plan.Name = gdRentConfig, "first"
		stale := ownerResult(t, old.sshCheck(m, "first"))
		closeOwnerGuide(t, m, old)
		current := ownerGuide(t, m)
		current.step, current.plan.Name = gdRentConfig, "second"
		fresh := current.sshCheck(m, "second")
		_ = m.update(stale)
		if m.modal != current || !current.run {
			t.Fatal("old SSH success completed an unexecuted current request")
		}
		_ = m.update(ownerResult(t, fresh))
		if w, ok := m.modal.(*wizard); !ok || w.host.Name != "second" {
			t.Fatal("current SSH success did not prepare the correct host")
		}
	})
}

func TestGuideFactsRequireRequestPhaseAndUnchangedInput(t *testing.T) {
	t.Run("new-request", func(t *testing.T) {
		m, _, _, be, _ := newHandoffCommandModel(t)
		local := be.fakeBackend.Local().(*fakeLocal)
		g := ownerGuide(t, m)
		g.step, local.key = gdRentKey, "old-key"
		stale := ownerResult(t, g.findKey(m))
		local.key = "current-key"
		fresh := g.findKey(m)
		_ = m.update(stale)
		if g.plan.Key != "" {
			t.Fatal("older request filled the current key")
		}
		_ = m.update(ownerResult(t, fresh))
		if g.plan.Key != "current-key" {
			t.Fatal("current key request was lost")
		}
	})
	t.Run("phase", func(t *testing.T) {
		m, _, _, _, _ := newHandoffCommandModel(t)
		g := ownerGuide(t, m)
		g.step = gdRentUser
		stale := ownerResult(t, g.sshCheck(m, "admin@first"))
		g.step, g.plan.Name = gdRentConfig, "second"
		_ = m.update(stale)
		if m.modal != g || !g.run || g.step != gdRentConfig {
			t.Fatal("SSH reply from an earlier phase skipped the config check")
		}
	})
	for _, edit := range []string{"key", "paste"} {
		t.Run(edit, func(t *testing.T) {
			m, _, _, _, _ := newHandoffCommandModel(t)
			g := ownerGuide(t, m)
			g.step, g.in = gdRentName, newInput("first", "")
			pending := g.nameServer(m)
			if edit == "key" {
				_ = g.key(m, tea.KeyPressMsg{Code: 'x', Text: "x"})
			} else {
				g.paste("x")
			}
			_ = m.update(ownerResult(t, pending))
			if g.step != gdRentName || g.in.value() != "firstx" {
				t.Fatal("reply for earlier input skipped the edited name")
			}
		})
	}
	t.Run("busy-paste", func(t *testing.T) {
		m, _, _, _, _ := newHandoffCommandModel(t)
		g := ownerGuide(t, m)
		g.step, g.in = gdRentUser, newInput("admin", "")
		pending := g.sshCheck(m, "admin@first")
		before := g.scope()
		g.paste("other")
		if g.in.value() != "admin" || g.scope() != before {
			t.Fatal("paste edited a running SSH request")
		}
		_ = m.update(ownerResult(t, pending))
		if g.step != gdRentName {
			t.Fatal("busy paste invalidated the current SSH request")
		}
	})
}

func TestGuideCursorAndEmptyPasteKeepCurrentLookup(t *testing.T) {
	for _, edit := range []string{"cursor", "empty-paste", "filtered-paste"} {
		t.Run(edit, func(t *testing.T) {
			m, _, _, _, _ := newHandoffCommandModel(t)
			g := ownerGuide(t, m)
			g.step, g.in = gdRentName, newInput("first", "")
			pending := g.nameServer(m)
			switch edit {
			case "cursor":
				_ = g.key(m, tea.KeyPressMsg{Code: tea.KeyLeft})
			case "empty-paste":
				g.paste("")
			case "filtered-paste":
				g.paste("\x00")
			}
			if g.in.value() != "first" {
				t.Fatal("fixture changed the submitted name")
			}
			_ = m.update(ownerResult(t, pending))
			if g.step != gdRentConfig {
				t.Fatal("unchanged input discarded its current lookup")
			}
		})
	}
}

func TestGuideCreateReplyDoesNotReplaceAnotherGuide(t *testing.T) {
	m, _, _, be, _ := newHandoffCommandModel(t)
	first := ownerGuide(t, m)
	ownerChoose(t, m, first, onboard.Docker)
	first.in.set("first")
	stale := ownerResult(t, first.create(m)).(createdMsg)
	first.halt()
	m.modal = nil
	second := ownerGuide(t, m)
	ownerChoose(t, m, second, onboard.Lima)
	second.in.set("second")
	fresh := ownerResult(t, second.create(m)).(createdMsg)
	if stale.scope.seq != fresh.scope.seq {
		t.Fatal("test missed the equal-sequence collision")
	}
	done, scope := second.done, second.scope()
	_ = m.update(stale)
	if m.modal != second || second.done != done || second.scope() != scope || !second.run {
		t.Fatal("old create reply adopted the wrong host or lost current cleanup ownership")
	}
	_ = m.update(fresh)
	if w, ok := m.modal.(*wizard); !ok || w.host.Name != "second" || w.host.Machine.Provider != onboard.Lima {
		t.Fatal("current create result did not prepare the correct host")
	}
	if len(be.hosts) != 3 || be.hosts[1].Name != "first" || be.hosts[2].Name != "second" {
		t.Fatalf("rejecting stale UI reply changed completed saved hosts: %+v", be.hosts)
	}
}

func TestGuideStaleCreateCannotClearActiveCleanupOwner(t *testing.T) {
	m, _, _, be, _ := newHandoffCommandModel(t)
	first := ownerGuide(t, m)
	ownerChoose(t, m, first, onboard.Docker)
	first.in.set("first")
	stale := ownerResult(t, first.create(m)).(createdMsg)
	first.halt()
	m.modal = nil
	second := ownerGuide(t, m)
	ownerChoose(t, m, second, onboard.Docker)
	second.in.set("second")
	local := be.fakeBackend.Local().(*fakeLocal)
	local.block = true
	started := make(chan struct{})
	m.be = ownerCreateBackend{Backend: be, local: ownerCreateLocal{Local: be.Local(), started: started}}
	command := second.create(m)
	reply := make(chan tea.Msg, 1)
	go func() { reply <- command() }()
	defer func() { second.halt(); <-reply }()
	<-started
	done := second.done
	_ = m.update(stale)
	if second.done != done || !second.run || m.modal != second {
		t.Fatal("stale reply discarded another request's cleanup owner")
	}
}

func TestGuideStopKeepsCompletedHostWithoutFurtherPreparation(t *testing.T) {
	m, _, _, be, _ := newHandoffCommandModel(t)
	g := ownerGuide(t, m)
	ownerChoose(t, m, g, onboard.Docker)
	g.in.set("made")
	msg := ownerResult(t, g.create(m)).(createdMsg)
	_ = g.back(m)
	if m.modal != g || !g.run || !g.stopping {
		t.Fatal("stop lost ownership before the completed create reply arrived")
	}
	calls := be.callList()
	executeHandoffCommand(m.update(msg))
	if m.modal != nil || !reflect.DeepEqual(calls, be.callList()) {
		t.Fatal("stop started new host preparation")
	}
	if len(be.hosts) != 2 || be.hosts[1].Name != "made" || !strings.Contains(m.toasts.shown[0].text, "host is saved") {
		t.Fatal("stop deleted or hid the completed creation outcome")
	}
	if h := m.hostByName("made"); h == nil || h.conn != nil || h.status != stOffline {
		t.Fatal("saved host was not listed without a connection")
	}
}
