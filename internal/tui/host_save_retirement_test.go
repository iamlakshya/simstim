package tui

import (
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestCommonHostSaveRetiresNormalAcknowledgements(t *testing.T) {
	m, _, h := hostSaveFixture(t)
	for range 200 {
		saved := hostSaveReply(t, m.saveHosts(h.host, false)())
		_, _ = m.Update(saved)
		if n := len(m.hostSaves.snapshot()); n != 0 {
			t.Fatalf("normal delivery retained %d completed operations", n)
		}
	}
}

func TestUninvokedCommonHostSaveRetainsNoOperation(t *testing.T) {
	m, be, h := hostSaveFixture(t)
	loads, saves := be.loadCalls.Load(), be.saveCalls.Load()
	cmd := m.saveHosts(h.host, true)
	m.pauseBg()
	m.Close()
	if msg := cmd(); msg != nil {
		t.Fatalf("an uninvoked command ran after Close: %#v", msg)
	}
	if be.loadCalls.Load() != loads || be.saveCalls.Load() != saves || len(m.hostSaves.snapshot()) != 0 {
		t.Fatal("uninvoked save did backend work or retained a completion")
	}
}

func TestCommonHostSaveRejectsAnotherModelsToken(t *testing.T) {
	first, _, h := hostSaveFixture(t)
	second, _, _ := hostSaveFixture(t)
	saved := hostSaveReply(t, first.saveHosts(h.host, true)())
	_, _ = second.Update(saved)
	if second.hostByName(h.name()) == nil || hostSaveToastCount(second, "Forgot forgotten.") != 0 {
		t.Fatal("a completion from another model removed this model's host")
	}
	if len(first.hostSaves.snapshot()) != 1 || len(second.hostSaves.snapshot()) != 0 {
		t.Fatal("foreign delivery changed completion ownership")
	}
	first.pauseBg()
	_ = resumeHostSaveFixture(t, first, "exec")
	if first.hostByName(h.name()) != nil {
		t.Fatal("the owning model lost its completion after foreign delivery")
	}
}

func TestCommonHostSaveUsesCanonicalStoredResult(t *testing.T) {
	m, _, h := hostSaveFixture(t)
	saved := hostSaveReply(t, m.saveHosts(h.host, true)())
	other := m.hostByName("kept")
	saved.host, saved.remove, saved.err = other.host, false, errors.New("not the stored result")
	w := newWizard()
	w.host, w.step, w.run, w.seq = other.host, wzReady, true, 1
	m.modal = w
	saved.owner, saved.seq = w, w.seq
	_, _ = m.Update(saved)
	if m.hostByName(h.name()) != nil || m.hostByName(other.name()) == nil || w.saved {
		t.Fatal("mutable envelope fields displaced the canonical common result")
	}
	if hostSaveToastCount(m, "Forgot forgotten.") != 1 || hostSaveToastCount(m, "not the stored result") != 0 {
		t.Fatal("the completion reported mutable envelope data")
	}
}

func TestWizardHostSaveRetainsSeparateResultGuards(t *testing.T) {
	m, _, w, _, _ := newHandoffCommandModel(t)
	setup := ownerPreparedHost(t, m, w)
	saved := ownerResult(t, m.onSetup(setup)).(hostSavedMsg)
	if saved.owner != w || saved.completion != nil || len(m.hostSaves.snapshot()) != 0 {
		t.Fatal("wizard save entered the common completion collection")
	}
	_, _ = m.Update(saved)
	if !w.saved || m.hostByName("new") == nil {
		t.Fatal("the existing wizard-owned success was not delivered")
	}
	stale := m.completeHostSave(hostSavedMsg{owner: w, seq: w.seq - 1, host: client.Host{Name: "other", SSH: "other"}})
	_, _ = m.Update(stale)
	if m.hostByName("other") != nil || len(m.hostSaves.snapshot()) != 0 {
		t.Fatal("the common finalizer bypassed a stale wizard result guard")
	}
}

func TestDroppedWizardResultCannotEnterCommonReplay(t *testing.T) {
	m, _, h := hostSaveFixture(t)
	w := newWizard()
	w.host, w.step, w.run, w.seq = h.host, wzReady, true, 2
	m.modal = w
	_ = m.completeHostSave(hostSavedMsg{owner: w, seq: 1, host: h.host, remove: true})
	m.pauseBg()
	_ = resumeHostSaveFixture(t, m, "exec")
	if m.hostByName(h.name()) == nil || hostSaveToastCount(m, "Forgot forgotten.") != 0 {
		t.Fatal("the common replay bypassed a wizard-owned result guard")
	}
}

func TestClosedModelIgnoresLateUntrackedHostSave(t *testing.T) {
	m, _, h := hostSaveFixture(t)
	m.Close()
	m.toasts = toastStack{}
	_, _ = m.Update(hostSavedMsg{host: h.host, remove: true})
	if m.hostByName(h.name()) == nil || len(m.toasts.shown)+len(m.toasts.queue) != 0 {
		t.Fatal("a late untracked host-save result changed the closed model")
	}
}
