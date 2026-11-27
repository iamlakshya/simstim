package tui

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func fakeRootPreview(tier client.Tier) *client.HostPrepareRootPreviewV1 {
	mode := client.RootRun
	if tier == client.TierShared {
		mode = client.RootPrint
	}
	return &client.HostPrepareRootPreviewV1{
		Tier:        tier,
		Needed:      true,
		DefaultMode: mode,
		Script: client.RootScript{
			SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			Steps: []client.RootStep{{
				ID:     client.RootKVMGroup,
				Reason: "Add pi to the kvm group",
				Line:   "usermod -aG kvm pi",
			}},
		},
	}
}

func fakeKVMBlockedProbe() *api.CapProbe {
	return &api.CapProbe{
		OS: "linux", Arch: "arm64", Kernel: "6.8.0", User: "pi", UID: 1000, UserNS: true, Sudo: true,
		KVM: api.KVM{Present: true},
		Walls: []api.WallProbe{
			{Driver: "gvisor", Level: "gvisor", OK: true, CanPrepare: true, Version: "runsc"},
			{Driver: "chv", Level: "microvm", OK: false, Reason: "no /dev/kvm access", Facts: map[string]string{"root_blocker": "kvm_access"}},
		},
	}
}

func TestWizardTenStepsPrepareAutoAdvancePersistsFloor(t *testing.T) {
	be := newFakeBackend()
	be.pinUnknown = true
	be.probe = client.Platform{OS: "linux", Arch: "arm64"}
	hs := newHarness(t, be, 80, 24)

	hs.key("a")
	if out := plainScreen(hs.m); !strings.Contains(out, "step 1 of 10") {
		t.Fatalf("where step label:\n%s", out)
	}
	hs.typeText("pi@raspberry")
	hs.key("enter")
	if out := plainScreen(hs.m); !strings.Contains(out, "step 2 of 10") {
		t.Fatalf("test step label:\n%s", out)
	}
	w := hs.m.modal.(*wizard)
	if w.ask == nil || w.ask.Fingerprint != fakeHostKey {
		t.Fatalf("unknown-key question lost the exact fingerprint: %+v", w.ask)
	}
	noCall(t, be, "probe ")
	noCall(t, be, "save ")
	hs.key("enter") // Confirm the displayed fingerprint before any connection.
	if w.ask != nil || w.pin == nil || w.pin.Fingerprint != fakeHostKey || w.want != fakeHostKey {
		t.Fatalf("confirmed fingerprint changed: pin=%+v want=%q ask=%+v", w.pin, w.want, w.ask)
	}
	hs.key("enter")
	if out := plainScreen(hs.m); !strings.Contains(out, "step 3 of 10") {
		t.Fatalf("tier step label:\n%s", out)
	}
	hs.key("1")
	if out := plainScreen(hs.m); !strings.Contains(out, "step 4 of 10") {
		t.Fatalf("install step label:\n%s", out)
	}
	hs.key("enter")

	w, ok := hs.m.modal.(*wizard)
	if !ok || w.step != wzReady || !w.saved || !w.sandboxTested {
		t.Fatalf("expected saved & tested wzReady: modal=%T step=%v saved=%v tested=%v err=%v", hs.m.modal, w.step, w.saved, w.sandboxTested, w.err)
	}
	out := plainScreen(hs.m)
	for _, want := range []string{"step 7 of 10", "raspberry is ready.", "Floor", "gvisor"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q on ready screen:\n%s", want, out)
		}
	}
	order(t, be,
		"stage raspberry",
		"pin raspberry "+fakeHostKey,
		"probe raspberry",
		"install raspberry",
		"save raspberry pin="+fakeHostKey,
		"prepare probe raspberry",
		"prepare runtime raspberry",
		"inspect root raspberry",
		"prepare boot raspberry",
	)
	hc := hs.m.hostByName("raspberry")
	if hc == nil || hc.host.Prepared == nil || hc.host.WallFloor != "gvisor" {
		t.Fatalf("launcher host did not receive prepared floor: %+v", hc)
	}
	be.mu.Lock()
	entry := be.tiers["raspberry"]
	be.mu.Unlock()
	if entry.Key != fakeHostKey || entry.Tier != client.TierOwn {
		t.Fatalf("registration did not save the exact confirmed key and tier: %+v", entry)
	}
}

func TestWizardSandboxesAndRootRunUpgradeFloor(t *testing.T) {
	be := newFakeBackend()
	be.probe = client.Platform{OS: "linux", Arch: "arm64"}
	be.capProbe = fakeKVMBlockedProbe()
	be.rootPreview = fakeRootPreview(client.TierOwn)
	be.prepareBootLevel = "gvisor"
	be.rootRunHook = func() {
		be.prepareBootLevel = "microvm"
	}

	hs := newHarness(t, be, 80, 24)
	hs.key("a")
	hs.typeText("pi@raspberry")
	hs.key("enter", "enter", "1", "enter")

	w := hs.m.modal.(*wizard)
	if w.step != wzSandboxes || !w.saved {
		t.Fatalf("expected pause on wzSandboxes: step=%v saved=%v err=%v", w.step, w.saved, w.err)
	}
	if out := plainScreen(hs.m); !strings.Contains(out, "step 5 of 10") || !strings.Contains(out, "enter root step") {
		t.Fatalf("sandboxes view:\n%s", out)
	}

	// Enter opens Root step (step 6 of 10); Esc goes back to Sandboxes (step 5 of 10).
	hs.key("enter")
	if w.step != wzRoot {
		t.Fatalf("expected wzRoot, got %v", w.step)
	}
	if out := plainScreen(hs.m); !strings.Contains(out, "step 6 of 10") || !strings.Contains(out, "usermod -aG kvm pi") {
		t.Fatalf("root view:\n%s", out)
	}
	hs.key("esc")
	if w.step != wzSandboxes {
		t.Fatalf("Esc on wzRoot should return to wzSandboxes, got %v", w.step)
	}

	// Enter -> Root step -> Enter runs sudo via exec handoff, then runs test boot and reaches Ready.
	hs.key("enter", "enter")
	if w.step != wzReady || !w.sandboxTested || w.host.WallFloor != "microvm" {
		t.Fatalf("after root run: step=%v tested=%v floor=%q err=%v", w.step, w.sandboxTested, w.host.WallFloor, w.err)
	}
	order(t, be, "inspect root raspberry", "root seal raspberry", "root run raspberry", "prepare boot raspberry")
}

func TestWizardSharedHostRootStepNeverDonatesEarlierConsent(t *testing.T) {
	be := newFakeBackend()
	be.probe = client.Platform{OS: "linux", Arch: "arm64"}
	be.rootPreview = fakeRootPreview(client.TierShared)
	be.prepareNoWall = true
	be.prepareAfterRoot = "microvm"

	hs := newHarness(t, be, 80, 24)
	hs.key("a")
	hs.typeText("pi@raspberry")
	hs.key("enter", "enter", "2", "enter") // 2 = Someone else (shared)

	w := hs.m.modal.(*wizard)
	if w.step != wzSandboxes {
		t.Fatalf("expected wzSandboxes, got %v", w.step)
	}
	hs.key("enter")
	if w.step != wzRoot {
		t.Fatalf("expected wzRoot, got %v", w.step)
	}
	// On a shared host, default Enter prints rather than running sudo.
	if out := plainScreen(hs.m); !strings.Contains(out, "enter print script") {
		t.Fatalf("shared host root view did not default to print:\n%s", out)
	}
	// Pressing "r" explicitly requests sudo run, which requires typing the host name in grantQuestion!
	hs.key("r")
	gq, ok := hs.m.modal.(*grantQuestion)
	if !ok || gq.request.question.Kind != client.ConfirmHostName || gq.request.question.Host != "raspberry" {
		t.Fatalf("shared host root run did not open ConfirmHostName grantQuestion: %T %+v", hs.m.modal, gq)
	}
	noCall(t, be, "root run ")
	// Cancel the grant question: root step must NOT run.
	hs.key("esc")
	if hs.m.modal != w || w.step != wzRoot {
		t.Fatalf("cancelling grant question lost wizard root step: modal=%T step=%v", hs.m.modal, w.step)
	}
	noCall(t, be, "root run ")

	// Now press Enter to print the script; with no runnable wall, floor stays unset and Ready is honest.
	hs.key("enter")
	if w.step != wzReady || w.sandboxTested {
		t.Fatalf("after print + no wall: step=%v tested=%v err=%v", w.step, w.sandboxTested, w.err)
	}
	out := plainScreen(hs.m)
	if strings.Contains(out, "raspberry is ready.") {
		t.Fatalf("printed no-wall state claimed ready:\n%s", out)
	}
	for _, want := range []string{"raspberry is saved; sandbox wall not ready yet.", "not prepared (microvm after root step)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing honest state %q:\n%s", want, out)
		}
	}
	hc := hs.m.hostByName("raspberry")
	if hc == nil || hc.host.Prepared != nil || hc.host.WallFloor != "" {
		t.Fatalf("no-wall preparation mutated floor or prepared flag: %+v", hc)
	}
}

func TestWizardBootFailureLeavesFloorUnchangedAndAllowsRetryOrContinue(t *testing.T) {
	be := newFakeBackend()
	be.probe = client.Platform{OS: "linux", Arch: "arm64"}
	be.prepareBootErr = &client.UserError{
		Msg:  "gvisor test boot exited with status 1",
		Hint: "Check runsc on the host, then try again.",
		Code: "sandbox_prepare_failed",
		Kind: client.KindFailed,
	}

	hs := newHarness(t, be, 80, 24)
	hs.key("a")
	hs.typeText("pi@raspberry")
	hs.key("enter", "enter", "1", "enter")

	w := hs.m.modal.(*wizard)
	if w.step != wzSandboxes || w.err == nil || w.sandboxTested {
		t.Fatalf("expected boot failure on wzSandboxes: step=%v err=%v tested=%v", w.step, w.err, w.sandboxTested)
	}
	hc := hs.m.hostByName("raspberry")
	if hc == nil || hc.host.Prepared != nil || hc.host.WallFloor != "" {
		t.Fatalf("failed boot wrote floor on saved host: %+v", hc)
	}

	// Pressing "s" continues to Ready with honest non-ready state and no floor.
	hs.key("s")
	if w.step != wzReady || w.sandboxTested {
		t.Fatalf("continue after failed boot: step=%v tested=%v", w.step, w.sandboxTested)
	}
	out := plainScreen(hs.m)
	if strings.Contains(out, "raspberry is ready.") || !strings.Contains(out, "raspberry is saved; sandbox wall not ready yet.") {
		t.Fatalf("failed boot claimed ready:\n%s", out)
	}
}

func TestWizardRefusesABARemoveReaddBetweenPreparePhases(t *testing.T) {
	be := newFakeBackend()
	be.probe = client.Platform{OS: "linux", Arch: "arm64"}
	be.rootPreview = fakeRootPreview(client.TierOwn)

	hs := newHarness(t, be, 80, 24)
	hs.key("a")
	hs.typeText("pi@raspberry")
	hs.key("enter", "enter", "1", "enter")

	w := hs.m.modal.(*wizard)
	if w.step != wzSandboxes {
		t.Fatalf("expected wzSandboxes, got %v", w.step)
	}

	// Simulate concurrent remove + re-add of "raspberry" before Phase 3/4 runs.
	old := w.host
	if _, err := be.UpdateHosts(client.HostChange{Was: &old}); err != nil {
		t.Fatal(err)
	}
	readded := client.Host{Name: "raspberry", SSH: "attacker@other"}
	if _, err := be.UpdateHosts(client.HostChange{Now: &readded}); err != nil {
		t.Fatal(err)
	}

	hs.key("s") // skip root -> attempts FinishPrepareRoot on original target
	if w.err == nil || w.sandboxTested {
		t.Fatalf("expected ABA target refusal, got err=%v tested=%v", w.err, w.sandboxTested)
	}
	noCall(t, be, "prepare boot ")
}

func TestWizardPrepareRefusesChangedKeyAfterExplicitConfirmation(t *testing.T) {
	be := newFakeBackend()
	be.probe = client.Platform{OS: "linux", Arch: "arm64"}
	be.pinUnknown = true
	hs := newHarness(t, be, 80, 24)
	hs.key("a")
	hs.typeText("pi@raspberry")
	hs.key("enter")
	w := hs.m.modal.(*wizard)
	if w.ask == nil || w.ask.Fingerprint != fakeHostKey {
		t.Fatal("the exact unknown key was not shown for confirmation")
	}
	be.mu.Lock()
	be.pinKey = "SHA256:" + strings.Repeat("m", 43)
	be.mu.Unlock()
	hs.key("enter")
	if w.err == nil || w.ask != nil || w.pin != nil || w.saved || w.sandboxTested {
		t.Fatalf("different second key advanced the wizard: step=%v err=%v pin=%+v saved=%v tested=%v", w.step, w.err, w.pin, w.saved, w.sandboxTested)
	}
	failure, ok := w.err.(*client.UserError)
	if !ok || failure.Kind != client.KindRefused || failure.Code != "host_key_mismatch" {
		t.Fatalf("different second key did not retain its refusal: %v", w.err)
	}
	order(t, be, "pin raspberry ", "pin raspberry "+fakeHostKey)
	for _, prefix := range []string{"probe ", "install ", "dial ", "save ", "prepare "} {
		noCall(t, be, prefix)
	}
	be.mu.Lock()
	defer be.mu.Unlock()
	if len(be.hosts) != 0 || len(be.tiers) != 0 || len(be.saved) != 0 || be.staged["raspberry"] != "" {
		t.Fatal("a refused key wrote a saved host, tier, pin or prepared floor")
	}
}
