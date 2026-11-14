package client

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestHostRegistrationAbsentSourceDoesNotCreateHostsFile(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "first", SSH: "first.example"}
	ctx, abort, err := StagePinOwned(t.Context(), h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	observation, err := ObserveHostRegistrationContext(ctx, HostChange{Kind: "host add", Now: &h})
	if err != nil {
		t.Fatal(err)
	}
	if observation.source.exists || observation.source.generation != "" {
		t.Fatal("the first unsaved registration invented a stored generation")
	}
	if _, err := os.Lstat(hostsPath()); !os.IsNotExist(err) {
		t.Fatalf("capturing a pre-consent observation created hosts.toml: %v", err)
	}
	if err := SaveHosts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := hostObservationAttempt(WithHostStateObservation(ctx, observation), h, "baseline.json", &called); err == nil || called {
		t.Fatal("publishing even empty membership failed to expire an absent source")
	}
	if _, err := os.Lstat(hostLockPath(h.Name)); !os.IsNotExist(err) {
		t.Fatalf("an expired absent source created an OP: %v", err)
	}
}

func TestHostStateReadMigratesExistingLegacyMembership(t *testing.T) {
	stateFDTestHome(t)
	if err := os.MkdirAll(ConfigDir(), 0700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte("# existing valid legacy hosts\n[[host]]\nname = \"box\"\nssh = \"legacy.example\"\n")
	if err := os.WriteFile(hostsPath(), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	h := Host{Name: "box", SSH: "legacy.example"}
	if _, found, err := LoadSandboxPolicy(t.Context(), h, "abcd2345"); err != nil || found {
		t.Fatalf("a pure current local read failed on a valid legacy membership: found=%v, %v", found, err)
	}
	generation, err := currentMembershipGeneration(t.Context())
	if err != nil || !ValidHostID(generation) {
		t.Fatalf("legacy migration did not publish a canonical generation: %q, %v", generation, err)
	}
	saved, err := FindHost(h.Name)
	if err != nil || !SameEntry(saved, h) {
		t.Fatalf("legacy migration changed the saved host values: %v", err)
	}
}

func TestHostStateMalformedOrNewerMarkerIsPreserved(t *testing.T) {
	for _, marker := range []string{"# membership-generation: invalid", "# membership-generation-v2: unknown"} {
		t.Run(marker, func(t *testing.T) {
			stateFDTestHome(t)
			if err := os.MkdirAll(ConfigDir(), 0700); err != nil {
				t.Fatal(err)
			}
			original := []byte(marker + "\n[[host]]\nname = \"box\"\nssh = \"legacy.example\"\n")
			if err := os.WriteFile(hostsPath(), original, 0600); err != nil {
				t.Fatal(err)
			}
			if _, unlock, err := LockHost(t.Context(), "box", "state read"); err == nil {
				unlock()
				t.Fatal("an unknown marker was adopted or rewritten as legacy")
			}
			if got, err := os.ReadFile(hostsPath()); err != nil || !bytes.Equal(got, original) {
				t.Fatalf("unknown membership marker was not preserved: %q, %v", got, err)
			}
		})
	}
}

func TestHostOPInitialIntentRefusesChangedGenerationEvenForUnchangedTarget(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "box", SSH: "original.example"}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	originalTake := takeLock
	defer func() { takeLock = originalTake }()
	changed := false
	var fixtureErr error
	takeLock = func(file *os.File) error {
		if filepath.Base(file.Name()) == filepath.Base(hostLockPath(h.Name)) && !changed {
			changed = true
			fixtureErr = SaveHosts(t.Context(), []Host{h, {Name: "other", SSH: "other.example"}})
			if fixtureErr != nil {
				return fixtureErr
			}
		}
		return originalTake(file)
	}
	if _, unlock, err := LockHost(t.Context(), h.Name, "old preopened intent"); err == nil {
		unlock()
		t.Fatal("SameEntry after a changed generation freshened the preopened OP")
	}
	if fixtureErr != nil || !changed {
		t.Fatalf("the unrelated-write fixture did not run: %v", fixtureErr)
	}
	if _, err := os.Lstat(hostLockPath(h.Name) + ".who"); !os.IsNotExist(err) {
		t.Fatalf("a stale preopened intent wrote a diagnostic: %v", err)
	}
}

func TestHostOPExistingObservationDoesNotRefreshAfterOpen(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "box", SSH: "metadata.example"}
	ctx, end, err := withHostPublicationObservation(t.Context(), HostChange{Kind: "host save", Now: &h})
	if err != nil {
		t.Fatal(err)
	}
	defer end()
	originalTake := takeLock
	defer func() { takeLock = originalTake }()
	changed := false
	var fixtureErr error
	takeLock = func(file *os.File) error {
		if filepath.Base(file.Name()) == filepath.Base(hostLockPath(h.Name)) && !changed {
			changed = true
			fixtureErr = SaveHosts(t.Context(), nil)
			if fixtureErr != nil {
				return fixtureErr
			}
		}
		return originalTake(file)
	}
	if _, unlock, err := LockHost(ctx, h.Name, "bound old intent"); err == nil {
		unlock()
		t.Fatal("the original observation refreshed its source after opening OP")
	}
	if fixtureErr != nil || !changed {
		t.Fatalf("the existing-source fixture did not run: %v", fixtureErr)
	}
	if _, err := os.Lstat(hostLockPath(h.Name) + ".who"); !os.IsNotExist(err) {
		t.Fatalf("a failed Phase C wrote a diagnostic: %v", err)
	}
}
