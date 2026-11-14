package client

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHostMembershipCanonicalUpdateSharesActualLeaseWithStatePreparation(t *testing.T) {
	h, ctx, _, _ := hostObservationFixture(t)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	called := false
	err := updateHostsFileContext(ctx, func(locked context.Context, hosts []Host) ([]Host, hostFileHooks, error) {
		if err := requireHostMembershipContext(locked); err != nil {
			return nil, hostFileHooks{}, err
		}
		err := withStateFile(locked, []string{"hosts", HostFileKey(h.Name)}, "removal.json", true, func([]byte, bool) ([]byte, error) {
			called = true
			return []byte("actual canonical preparation context"), nil
		})
		return hosts, hostFileHooks{}, err
	})
	if err != nil || !called {
		t.Fatalf("nested preparation reopened its already-held native membership lock: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(StateDir(), "hosts", HostFileKey(h.Name), "removal.json"))
	if err != nil || string(got) != "actual canonical preparation context" {
		t.Fatalf("the nested state write did not commit: %q, %v", got, err)
	}
}

func TestHostOPCheckpointModePreservesDiagnosticOnOrdinaryUnwind(t *testing.T) {
	h, ctx, _, unlock := hostObservationFixture(t)
	held := ctx.Value(heldKey{h.Name}).(*heldLock)
	path := hostLockPath(h.Name) + ".who"
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// This is only the unwind-mode witness. It does not prove receipt,
	// checkpoint framing or native terminal unlink authority.
	held.terminal.Store(true)
	unlock()
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("checkpoint error unwind removed its reconstruction diagnostic: %v", err)
	}
}
