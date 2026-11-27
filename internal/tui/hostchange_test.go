package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

// The launcher's saves and removes compare with the entry that it read
// (docs/contributing/cli-spec.md 11.3, rule 13): it holds no lock across
// a dialog or the add-host wizard, so another command may change the host
// meanwhile, and the change then changes nothing.

// saved reports whether be has a host named name in its file.
func saved(be *fakeBackend, name string) (client.Host, bool) {
	be.mu.Lock()
	defer be.mu.Unlock()
	i := slices.IndexFunc(be.hosts, func(h client.Host) bool { return h.Name == name })
	if i < 0 {
		return client.Host{}, false
	}
	return be.hosts[i], true
}

// replace puts h in be's file in place of the host of its name, as another
// command does.
func replace(be *fakeBackend, h client.Host) {
	be.mu.Lock()
	defer be.mu.Unlock()
	be.hosts = append(slices.DeleteFunc(be.hosts, func(o client.Host) bool { return o.Name == h.Name }), h)
}

// remove takes host name out of be's file, as host rm does.
func remove(be *fakeBackend, name string) {
	be.mu.Lock()
	defer be.mu.Unlock()
	be.hosts = slices.DeleteFunc(be.hosts, func(o client.Host) bool { return o.Name == name })
}

// A remove confirmed after another command made the host again changes
// nothing: the new host stays in the file and on the screen, and the
// launcher says why.
func TestAStaleRemoveConfirmation(t *testing.T) {
	hs := home(t, 80, 24)
	hs.key("ctrl+o")
	hs.typeText("host remove")
	hs.key("enter")
	if _, ok := hs.m.modal.(*confirm); !ok {
		t.Fatalf("no confirmation: %T", hs.m.modal)
	}
	again := client.Host{Name: "atlas-dev", Machine: &client.Machine{Provider: "lima", ID: "atlas-dev", Created: "2"}}
	replace(hs.be, again)
	hs.key("enter")
	if h, ok := saved(hs.be, "atlas-dev"); !ok || !client.SameEntry(h, again) {
		t.Fatalf("the host made again: %+v, %v", h, ok)
	}
	if hs.m.hostByName("atlas-dev") == nil {
		t.Error("the host left the screen")
	}
	if !slices.ContainsFunc(toastTexts(hs.m), func(s string) bool {
		return strings.Contains(s, "Atlas-dev was made again while the launcher ran, so it was not forgotten")
	}) {
		t.Errorf("the toasts are %q", toastTexts(hs.m))
	}
}

// While a command holds the lock of the host, the launcher's remove is
// busy: the host stays in the file and on the screen, and the launcher
// says which command holds it.
func TestTheLauncherIsBusyWhileACommandHoldsTheLock(t *testing.T) {
	hs := home(t, 80, 24)
	hs.be.updateErr = &client.UserError{Msg: "atlas-dev is busy: host rm changes it", Hint: "Wait until it ends, then run the command again.", Code: "host_busy"}
	hs.key("ctrl+o")
	hs.typeText("host remove")
	hs.key("enter", "enter")
	if _, ok := saved(hs.be, "atlas-dev"); !ok || hs.m.hostByName("atlas-dev") == nil {
		t.Fatal("the launcher removed a busy host")
	}
	if !slices.ContainsFunc(toastTexts(hs.m), func(s string) bool { return strings.Contains(s, "busy: host rm changes it") }) {
		t.Errorf("the toasts are %q", toastTexts(hs.m))
	}
}

// runGuideCreate runs the launcher's guided create of the Lima host vm, from
// the welcome, and returns the harness and the wizard that connects it.
func runGuideCreate(t *testing.T, be *fakeBackend) (*harness, *wizard) {
	t.Helper()
	be.probe = client.Platform{OS: "linux", Arch: "arm64"}
	hs := newHarness(t, be, 80, 24)
	hs.key("enter", "down", "enter")
	hs.key("ctrl+u")
	hs.typeText("vm")
	hs.key("enter")
	hasCall(t, be, "create lima vm")
	w, ok := hs.m.modal.(*wizard)
	if !ok {
		t.Fatalf("after the create: %T", hs.m.modal)
	}
	return hs, w
}

// dials is how many times be dialled host name.
func dials(be *fakeBackend, name string) int {
	be.mu.Lock()
	defer be.mu.Unlock()
	return be.dials[name]
}

// The launcher's create saves the host, then the wizard installs the
// helper and saves it again. A host rm before the install, or just before
// that save, keeps the host removed: the save fails with host_changed.
// Esc then closes the wizard, and the host stays off the screen: the
// launcher reads the file again, and dials nothing.
func TestTheLauncherCreateKeepsARemovedHostRemoved(t *testing.T) {
	for _, when := range []string{"before the install", "before the save"} {
		be := newFakeBackend()
		switch when {
		case "before the install":
			be.beforeInstall = func(h client.Host) { remove(be, h.Name) }
		default:
			be.beforeUpdate = func(c client.HostChange) {
				if c.Now != nil && c.Was != nil {
					remove(be, c.Now.Name)
				}
			}
		}
		hs, w := runGuideCreate(t, be)
		var ue *client.UserError
		if !errors.As(w.err, &ue) || ue.Code != "host_changed" || ue.Msg != "vm was removed while the launcher ran, so it was not saved" || w.saved {
			t.Errorf("%s: the wizard says %+v, saved %v", when, w.err, w.saved)
		}
		before := dials(be, "vm")
		hs.key("esc")
		if hs.m.modal != nil || hs.m.hostByName("vm") != nil || dials(be, "vm") != before {
			t.Errorf("%s: after Esc, the modal is %T, vm on the screen %v, dials %d then %d",
				when, hs.m.modal, hs.m.hostByName("vm") != nil, before, dials(be, "vm"))
		}
		if _, ok := saved(be, "vm"); ok {
			t.Errorf("%s: the removed host came back", when)
		}
	}
}

// A remove that another command's change refused reads the host again:
// the next remove compares with the entry that is saved now, and works.
func TestARemoveAfterAnOutsideChange(t *testing.T) {
	hs := home(t, 80, 24)
	changed := client.Host{Name: "atlas-dev", SSH: "atlas-dev", Labels: map[string]string{"set": "from a terminal"}}
	replace(hs.be, changed)
	removeIt := func() {
		hs.key("ctrl+o")
		hs.typeText("host remove")
		hs.key("enter", "enter")
	}
	removeIt()
	if _, ok := saved(hs.be, "atlas-dev"); !ok {
		t.Fatal("the remove of a changed host went ahead")
	}
	if h := hs.m.hostByName("atlas-dev"); h == nil || !client.SameEntry(h.host, changed) {
		t.Fatalf("the launcher did not read the host again: %+v", h)
	}
	if !slices.ContainsFunc(toastTexts(hs.m), func(s string) bool { return strings.Contains(s, "changed while the launcher ran") }) {
		t.Errorf("the toasts are %q", toastTexts(hs.m))
	}
	removeIt()
	if _, ok := saved(hs.be, "atlas-dev"); ok || hs.m.hostByName("atlas-dev") != nil {
		t.Error("the second remove did not forget the host")
	}
}

// The add-host wizard saves a new host only while no host of its name is
// saved: one that a command saved meanwhile stays as it is.
func TestTheWizardKeepsAHostSavedMeanwhile(t *testing.T) {
	be := newFakeBackend()
	made := client.Host{Name: "box", Machine: &client.Machine{Provider: "docker", ID: "box", Created: "1"}}
	be.beforeUpdate = func(client.HostChange) { replace(be, made) }
	hs := newHarness(t, be, 80, 24)
	hs.m.motion = false
	w := newWizard()
	w.host = client.Host{Name: "box", SSH: "dev@203.0.113.10"}
	msg, ok := w.save(hs.m)().(hostSavedMsg)
	var ue *client.UserError
	if !ok || !errors.As(msg.err, &ue) || ue.Code != "host_exists" {
		t.Fatalf("the save over a host saved meanwhile: %+v", msg)
	}
	if h, _ := saved(be, "box"); !client.SameEntry(h, made) {
		t.Errorf("the wizard replaced the saved host: %+v", h)
	}
}
