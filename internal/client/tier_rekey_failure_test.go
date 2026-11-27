package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func tierRekeyFixture(t *testing.T) (Host, hostKey, hostKey) {
	t.Helper()
	dir := fakeSSH(t)
	old, next := newKey(t, "ssh-ed25519"), newKey(t, "ssh-ed25519")
	put(t, dir, "trust-log", trustLog(next, "trusted"))
	shows(t, dir, next)
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if err := writeKeys(pinPath(h.Name), h.Name, []hostKey{old}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTiersContext(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	return h, old, next
}

func TestRekeyTierFailurePreservesTheOldPin(t *testing.T) {
	for _, fault := range []string{"malformed", "directory", "write-error"} {
		t.Run(fault, func(t *testing.T) {
			h, old, _ := tierRekeyFixture(t)
			_, _, err := Rekey(context.Background(), h, "", func(string, string) error {
				assertHostFileLockHeld(t, hostLockPath(h.Name))
				if fault == "malformed" {
					return os.WriteFile(tiersPath(), []byte("[broken"), 0o600)
				}
				if fault == "write-error" {
					previous := writeSavedTierFile
					t.Cleanup(func() { writeSavedTierFile = previous })
					writeSavedTierFile = func(Tiers) error { return errors.New("tier save failed") }
					return nil
				}
				// Replacing the source by a directory must also refuse
				// before the new pin reaches its published pathname.
				backup := filepath.Join(t.TempDir(), "tiers")
				if err := os.Rename(tiersPath(), backup); err != nil {
					return err
				}
				return os.Mkdir(tiersPath(), 0o700)
			})
			if err == nil {
				t.Fatal("rekey succeeded without saving its restrictive tier")
			}
			pin, perr := PinOf(h.Name)
			if perr != nil || pin.Fingerprint != old.Fingerprint() {
				t.Fatalf("failed tier write replaced the pin: %+v err=%v", pin, perr)
			}
		})
	}
}

func TestRekeyPinFailureKeepsTheRestrictiveTier(t *testing.T) {
	h, _, next := tierRekeyFixture(t)
	backup := filepath.Join(t.TempDir(), "old-pin")
	_, _, err := Rekey(context.Background(), h, "", func(string, string) error {
		if err := os.Rename(pinPath(h.Name), backup); err != nil {
			return err
		}
		return os.Mkdir(pinPath(h.Name), 0o700)
	})
	if err == nil {
		t.Fatal("rekey succeeded after its pin path became a directory")
	}
	ts, err := readTiersFile()
	if err != nil {
		t.Fatal(err)
	}
	e, _ := ts.Entry(h.Name)
	if e.Tier != TierShared || e.Key != next.Fingerprint() {
		t.Fatalf("pin failure lost the restrictive guard: %+v", e)
	}
	// Even if the old pin comes back, the new recorded key refuses it.
	if err := os.Remove(pinPath(h.Name)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, pinPath(h.Name)); err != nil {
		t.Fatal(err)
	}
	var user *UserError
	if err := checkTierKeyPin(context.Background(), h.Name); !errors.As(err, &user) || user.Code != "host_key_unreadable" {
		t.Fatalf("old pin connected after failed replacement: %v", err)
	}
}

func TestRekeyRefusesAChangedSavedEntryBeforeProbing(t *testing.T) {
	h, old, _ := tierRekeyFixture(t)
	replacement := h
	replacement.SSH = "another.example"
	if err := SaveHosts(context.Background(), []Host{replacement}); err != nil {
		t.Fatal(err)
	}
	asked := false
	var user *UserError
	_, _, err := Rekey(context.Background(), h, "", func(string, string) error { asked = true; return nil })
	if !errors.As(err, &user) || user.Code != "host_changed" || asked {
		t.Fatalf("stale rekey: asked=%v err=%v", asked, err)
	}
	pin, err := PinOf(h.Name)
	if err != nil || pin.Fingerprint != old.Fingerprint() {
		t.Fatalf("stale rekey replaced its pin: %+v err=%v", pin, err)
	}
}
