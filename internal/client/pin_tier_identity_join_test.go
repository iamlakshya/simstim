package client

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetiredPinSaveComparesTheEntryWithoutChangingTiers(t *testing.T) {
	for _, change := range []string{"unchanged", "changed", "made again", "removed"} {
		t.Run(change, func(t *testing.T) {
			sshDirs(t)
			h := Host{Name: "box", Machine: &Machine{Provider: "docker", ID: "box", Created: "1"}}
			if err := SaveHosts(t.Context(), []Host{h}); err != nil {
				t.Fatal(err)
			}
			ctx, abort, err := StagePinOwned(t.Context(), h.Name)
			if err != nil {
				t.Fatal(err)
			}
			defer abort()
			_, abortNext, err := StagePinOwned(t.Context(), h.Name)
			if err != nil {
				t.Fatal(err)
			}
			defer abortNext()
			next := currentStage(h.Name)
			current := h
			switch change {
			case "changed":
				current.Labels = map[string]string{"edited": "yes"}
			case "made again":
				machine := *h.Machine
				machine.Created = "2"
				current.Machine = &machine
			}
			hosts := []Host{current}
			if change == "removed" {
				hosts = nil
			}
			if err := SaveHosts(t.Context(), hosts); err != nil {
				t.Fatal(err)
			}
			before, beforeErr := os.ReadFile(tiersPath())
			if beforeErr != nil && !errors.Is(beforeErr, os.ErrNotExist) {
				t.Fatal(beforeErr)
			}
			err = UpdateHosts(ctx, HostChange{Kind: "setup", Was: &h, Now: &h, Pin: true})
			var user *UserError
			if !errors.As(err, &user) {
				t.Fatalf("retired pin save = %v", err)
			}
			if change == "unchanged" {
				if user.Code != "cancelled" || user.Kind != KindCancelled {
					t.Fatalf("unchanged entry adopted a replacement stage: %+v", user)
				}
			} else if user.Code != "host_changed" || user.Kind != KindFailed || !strings.Contains(user.Msg, change) {
				t.Fatalf("stage hid saved-entry conflict %q: %+v", change, user)
			}
			after, afterErr := os.ReadFile(tiersPath())
			if afterErr != nil && !errors.Is(afterErr, os.ErrNotExist) {
				t.Fatal(afterErr)
			}
			if !bytes.Equal(before, after) || errors.Is(beforeErr, os.ErrNotExist) != errors.Is(afterErr, os.ErrNotExist) {
				t.Fatal("a retired save changed tiers before its CAS refusal")
			}
			if currentStage(h.Name) != next {
				t.Fatal("the retired save closed the replacement stage")
			}
			if _, err := os.Stat(next.path); err != nil {
				t.Fatalf("the replacement stage file changed: %v", err)
			}
			saved, err := LoadHosts()
			if err != nil || len(saved) != len(hosts) || len(saved) != 0 && !SameEntry(saved[0], current) {
				t.Fatalf("retired save changed the current host: %+v, %v", saved, err)
			}
		})
	}
}

func TestProviderPinKeepsItsOriginalSavedCASIdentity(t *testing.T) {
	dir := fakeSSH(t)
	key := newKey(t, "ssh-ed25519")
	put(t, dir, "trust-log", trustLog(key, "unknown"))
	shows(t, dir, key)
	h := Host{Name: "vm", Machine: &Machine{Provider: "pintest", ID: "vm", Created: "1"}}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTiersContext(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	tiersBefore, err := readTiersFile()
	if err != nil {
		t.Fatal(err)
	}
	entryBefore, had := tiersBefore.Entry(h.Name)
	if !had || entryBefore.Key != "" {
		t.Fatal("provider fixture already has a pinned tier key")
	}
	replacement := h
	replacement.Labels = map[string]string{"edited": "yes"}
	before := afterPinCommit
	afterPinCommit = func() {
		if err := SaveHosts(t.Context(), []Host{replacement}); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterPinCommit = before })
	_, err = HostTransport(t.Context(), h, true)
	var user *UserError
	if !errors.As(err, &user) || user.Code != "host_changed" || user.Kind != KindFailed || user.Msg != "vm changed while host test ran, so it was not saved" {
		t.Fatalf("provider route lost or refreshed its original saved entry: %v", err)
	}
	if now, err := FindHost(h.Name); err != nil || !SameEntry(now, replacement) {
		t.Fatalf("pin observation changed the newer saved entry: %+v, %v", now, err)
	}
	tiers, err := readTiersFile()
	entryAfter, has := tiers.Entry(h.Name)
	if err != nil || !has || !sameTierEntry(entryBefore, entryAfter) {
		t.Fatalf("pin observation changed the replacement's tier: %+v, %v", tiers, err)
	}
}

func TestProviderPinUpdatesTheOriginalSavedMachine(t *testing.T) {
	dir := fakeSSH(t)
	key := newKey(t, "ssh-ed25519")
	put(t, dir, "trust-log", trustLog(key, "unknown"))
	shows(t, dir, key)
	h := Host{Name: "vm", Machine: &Machine{Provider: "pintest", ID: "vm", Created: "1"}}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if _, err := HostTransport(t.Context(), h, true); err != nil {
		t.Fatalf("pin the saved machine through its provider route: %v", err)
	}
	if saved, err := FindHost(h.Name); err != nil || !SameEntry(saved, h) {
		t.Fatalf("pin observation replaced the saved machine with its SSH route: %+v, %v", saved, err)
	}
	tiers, err := readTiersFile()
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := tiers.Entry(h.Name)
	if !ok || entry.Key != key.Fingerprint() {
		t.Fatalf("the saved machine did not receive its observed pin: %+v", entry)
	}
}

func TestStaleTierEntryRefusesBeforeSSHWithoutMaskingCancellation(t *testing.T) {
	dir := fakeSSH(t)
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	pinFor(t, h.Name)
	stale := h
	stale.SSH = "elsewhere.example"
	_, err := Dial(WithoutInput(t.Context()), stale, false)
	var user *UserError
	if !errors.As(err, &user) || user.Code != "host_changed" || user.Kind != KindFailed {
		t.Fatalf("stale target bypassed the tier entry check: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Dial(WithoutInput(ctx), stale, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("saved-entry check masked cancellation: %v", err)
	}
	if calls := sshCalls(t, dir); len(calls) != 0 {
		t.Fatalf("a refused or cancelled stale target ran SSH: %q", calls)
	}
}

func TestForgetObstructionRetiresPinOnlyAfterTheCancellationGate(t *testing.T) {
	h, path, _ := wrapperForgetFixture(t)
	if err := writeKeys(pinPath(h.Name), h.Name, []hostKey{newKey(t, "ssh-ed25519")}); err != nil {
		t.Fatal(err)
	}
	pin, err := os.ReadFile(pinPath(h.Name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "busy"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	before := beforeHostsSave
	beforeHostsSave = cancel
	t.Cleanup(func() { beforeHostsSave = before })
	if err := UpdateHosts(ctx, HostChange{Kind: "host rm", Was: &h}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cleanup cancellation lost precedence: %v", err)
	}
	if got, err := os.ReadFile(pinPath(h.Name)); err != nil || !bytes.Equal(got, pin) {
		t.Fatalf("pre-cleanup cancellation retired the pin: %v", err)
	}
	beforeHostsSave = before
	if err := UpdateHosts(t.Context(), HostChange{Kind: "host rm", Was: &h}); err == nil || HasPin(h.Name) {
		t.Fatalf("failed cleanup left a connectable pin: %v, pinned=%v", err, HasPin(h.Name))
	}
	if now, err := FindHost(h.Name); err != nil || !SameEntry(now, h) {
		t.Fatalf("failed cleanup lost the retryable host: %+v, %v", now, err)
	}
	if _, err := os.Stat(filepath.Join(path, "busy")); err != nil {
		t.Fatalf("cleanup touched the obstructing folder: %v", err)
	}
}
