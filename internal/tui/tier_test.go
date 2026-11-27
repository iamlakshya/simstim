package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// The wizard's tier step saves the chosen tier and the host's ID, keeps the
// staged pin when Esc goes back to the test step, and marks a shared host's
// facts as reported by the host.
func TestWizardTierStepAndSharedLabels(t *testing.T) {
	be := newFakeBackend()
	be.probe = client.Platform{OS: "linux", Arch: "arm64"}
	hs := newHarness(t, be, 80, 24)
	hs.m.motion = false

	hs.key("a")
	hs.typeText("me@lab")
	hs.key("enter", "enter")
	w := hs.m.modal.(*wizard)
	if w.step != wzTier {
		t.Fatalf("step = %v, want wzTier", w.step)
	}
	screen := plainScreen(hs.m)
	for _, want := range []string{
		"Who else has root on lab?",
		"1 Only me",
		"own: gets your secrets when you ask",
		"2 Someone else",
		"shared: no secret unless you mark it",
		"3 A machine for one task",
		"throwaway: short-lived secrets only",
	} {
		if !strings.Contains(screen, want) {
			t.Errorf("tier sheet lacks %q:\n%s", want, screen)
		}
	}

	// Esc from wzTier goes back to wzTest without aborting the staged pin.
	hs.key("esc")
	if w.step != wzTest {
		t.Fatalf("after Esc from wzTier: step = %v", w.step)
	}
	noCall(t, be, "abort ")

	// Pick shared ("2") and finish the install.
	hs.key("enter", "2", "enter")
	if !w.saved || w.step != wzReady {
		t.Fatalf("not saved at wzReady: saved=%v step=%v err=%v", w.saved, w.step, w.err)
	}
	entry := be.tiers["lab"]
	if entry.Tier != client.TierShared || entry.HostID != "0123456789abcdef0123456789abcdef" || entry.Key != fakeHostKey {
		t.Fatalf("saved tier entry = %+v", entry)
	}
	if ready := plainScreen(hs.m); !strings.Contains(ready, "reported by lab") {
		t.Errorf("ready view lacks 'reported by lab':\n%s", ready)
	}

	// Close the wizard and go back to the hosts list: a shared host shows
	// "shared" in its row and "reported by lab" in the detail line and sheet.
	hs.key("esc")
	hs.m.screen, hs.m.cur = scrHosts, ""
	hostsOut := plainScreen(hs.m)
	for _, want := range []string{"lab       floor gvisor · now unknown · shared", "ssh me@lab · shared · reported by lab · Ubuntu 24.04"} {
		if !strings.Contains(hostsOut, want) {
			t.Errorf("hosts view lacks %q:\n%s", want, hostsOut)
		}
	}
	hs.key("i")
	detailsOut := plainScreen(hs.m)
	for _, want := range []string{"Tier       shared", "reported by lab"} {
		if !strings.Contains(detailsOut, want) {
			t.Errorf("details sheet lacks %q:\n%s", want, detailsOut)
		}
	}
}

// Guided create skips the tier question and records the provider's tier:
// shared for e2b, own for lima.
func TestGuidedCreateUsesProviderTier(t *testing.T) {
	be := newFakeBackend()
	be.probe = client.Platform{OS: "linux", Arch: "amd64"}
	l := be.Local().(*fakeLocal)
	l.e2bKey = "e2b_test"
	installs(be)
	hs := newHarness(t, be, 80, 24)
	hs.key("s")
	// Choose E2B ("4") and create "cloud".
	hs.key("4")
	g := hs.m.modal.(*guide)
	if g.kind != "e2b" {
		t.Fatalf("kind = %q, want e2b", g.kind)
	}
	hs.key("ctrl+u")
	hs.typeText("cloud")
	hs.key("enter")
	w, ok := hs.m.modal.(*wizard)
	if !ok || !w.saved || w.step != wzReady {
		t.Fatalf("after e2b create: modal=%T step=%v saved=%v", hs.m.modal, w.step, w.saved)
	}
	if got := be.tiers["cloud"].Tier; got != client.TierShared {
		t.Errorf("e2b tier = %s, want shared", got)
	}
}

// A host that answers with a different host ID is downgraded to shared in the
// launcher, marked "host ID changed", and raises a warning toast.
func TestLauncherDowngradesOnChangedHostID(t *testing.T) {
	be := labBackend()
	be.state["atlas-dev"].hostID = "fedcba9876543210fedcba9876543210"
	be.state["atlas-dev"].idChanged = true
	hs := newHarness(t, be, 100, 28)
	hs.m.motion = false

	h := hs.m.hostByName("atlas-dev")
	if h.tier != client.TierShared || !h.idChanged {
		t.Fatalf("atlas-dev: tier=%s idChanged=%v", h.tier, h.idChanged)
	}
	out := plainScreen(hs.m)
	for _, want := range []string{"shared · host ID changed", "ssh atlas-dev · shared · host ID changed · reported by atlas-dev"} {
		if !strings.Contains(out, want) {
			t.Errorf("hosts screen lacks %q:\n%s", want, out)
		}
	}
	texts := toastTexts(hs.m)
	if !slices.Contains(texts, "atlas-dev answers with a different host ID, so its tier is now shared.") {
		t.Errorf("toasts = %q", texts)
	}
	hasHint := false
	for _, ts := range hs.m.toasts.shown {
		if ts.hint == "If it is the same machine: "+brand.Name+" host set atlas-dev tier=own" {
			hasHint = true
		}
	}
	if !hasHint {
		t.Errorf("toast hints = %+v", hs.m.toasts.shown)
	}
}
