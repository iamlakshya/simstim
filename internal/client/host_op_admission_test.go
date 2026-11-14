package client

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestHostOPPreopenedReplacedInodeCannotOwnCurrentDomain(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "box", SSH: "original.example"}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	path := hostLockPath(h.Name)
	originalTake := takeLock
	defer func() { takeLock = originalTake }()
	swapped := false
	var fixtureErr error
	foreign := []byte("foreign replacement OP")
	takeLock = func(file *os.File) error {
		if filepath.Base(file.Name()) == filepath.Base(path) && !swapped {
			swapped = true
			if err := os.Rename(path, path+".original"); err != nil {
				fixtureErr = err
				return err
			}
			if err := os.WriteFile(path, foreign, 0600); err != nil {
				fixtureErr = err
				return err
			}
		}
		return originalTake(file)
	}
	if _, unlock, err := LockHost(t.Context(), h.Name, "preopened contender"); err == nil {
		unlock()
		t.Fatal("the old opened FD became owner of a replacement domain")
	}
	if !swapped || fixtureErr != nil {
		t.Fatalf("the source fixture did not complete the open-before-take replacement: %v", fixtureErr)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, foreign) {
		t.Fatalf("refusal changed the replacement OP: %q, %v", got, err)
	}
}

func TestHostOPUnlockPreservesReplacementDiagnosticAndIsIdempotent(t *testing.T) {
	h, _, _, oldUnlock := hostObservationFixture(t)
	who := hostLockPath(h.Name) + ".who"
	if err := os.Rename(who, who+".original"); err != nil {
		t.Fatal(err)
	}
	foreign := []byte("replacement diagnostic")
	if err := os.WriteFile(who, foreign, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(who)
	if err != nil {
		t.Fatal(err)
	}
	oldUnlock()
	_, nextUnlock, err := LockHost(t.Context(), h.Name, "new owner")
	if err != nil {
		t.Fatal(err)
	}
	defer nextUnlock()
	oldUnlock()
	got, readErr := os.ReadFile(who)
	after, statErr := os.Stat(who)
	if readErr != nil || statErr != nil || !bytes.Equal(got, foreign) || !os.SameFile(before, after) {
		t.Fatalf("an old unlock or new owner changed a foreign diagnostic: %q, %v, %v", got, readErr, statErr)
	}
}

func TestHostOPNormalRefusalPreservesLargeTerminalProofAndDiagnostic(t *testing.T) {
	h, _, _, unlock := hostObservationFixture(t)
	unlock()
	path := hostLockPath(h.Name)
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(hostTerminalPrefix); err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(8 << 20); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, release, err := LockHost(t.Context(), h.Name, "ordinary operation"); err == nil {
		release()
		t.Fatal("ordinary OP admission accepted a terminal proof")
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || after.Size() != before.Size() {
		t.Fatalf("ordinary refusal replaced or truncated the terminal proof: %v", err)
	}
	if _, err := os.Lstat(path + ".who"); !os.IsNotExist(err) {
		t.Fatalf("ordinary terminal refusal created a diagnostic: %v", err)
	}
}
