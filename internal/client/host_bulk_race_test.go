package client

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestHostBulkPreopenedOPRefusesRemoveAndIdenticalReaddABA(t *testing.T) {
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
			if fixtureErr = SaveHosts(t.Context(), nil); fixtureErr != nil {
				return fixtureErr
			}
			if fixtureErr = SaveHosts(t.Context(), []Host{h}); fixtureErr != nil {
				return fixtureErr
			}
		}
		return originalTake(file)
	}
	if _, unlock, err := LockHost(t.Context(), h.Name, "delayed old intent"); err == nil {
		unlock()
		t.Fatal("remove and identical re-add revived a preopened OP")
	}
	if !changed || fixtureErr != nil {
		t.Fatalf("the canonical ABA fixture did not complete: %v", fixtureErr)
	}
	if _, err := os.Lstat(hostLockPath(h.Name) + ".who"); !os.IsNotExist(err) {
		t.Fatalf("stale Phase C created a diagnostic: %v", err)
	}
}

func TestHostBulkReleasesProbeButBlocksDelayedPhaseCUntilCommit(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "box", SSH: "before.example"}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	old, err := openHostOperationFile(t.Context(), h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer old.close()
	originalHook := beforeHostsSave
	defer func() { beforeHostsSave = originalHook }()
	probed := make(chan struct{})
	commit := make(chan struct{})
	var releaseCommit sync.Once
	allowCommit := func() { releaseCommit.Do(func() { close(commit) }) }
	defer allowCommit()
	beforeHostsSave = func() { close(probed); <-commit }
	changed := cloneObservationHost(h)
	changed.SSH = "after.example"
	bulkDone := make(chan struct{})
	bulkResult := make(chan error, 1)
	go func() {
		defer close(bulkDone)
		bulkResult <- SaveHosts(t.Context(), []Host{changed})
	}()
	hostStateLifetimeAwait(t, probed)
	// The successful probe must already have released its OP FD although
	// membership remains held at the controlled pre-commit boundary.
	if err := takeLock(old.file); err != nil {
		t.Fatal("the bulk retained its probe instead of releasing it:", err)
	}
	lateDone := make(chan struct{})
	lateResult := make(chan error, 1)
	go func() {
		defer close(lateDone)
		lateResult <- old.checkAfterTake(t.Context(), h.Name, "delayed Phase C", &heldLock{native: old})
	}()
	hostStateLifetimeStaysBlocked(t, lateDone)
	if _, err := os.Lstat(hostLockPath(h.Name) + ".who"); !os.IsNotExist(err) {
		t.Fatalf("delayed Phase C wrote before membership commit: %v", err)
	}
	allowCommit()
	hostStateLifetimeAwait(t, bulkDone)
	if err := <-bulkResult; err != nil {
		t.Fatal(err)
	}
	hostStateLifetimeAwait(t, lateDone)
	if err := <-lateResult; err == nil {
		t.Fatal("the stale delayed taker proceeded after the bulk generation changed")
	}
}
