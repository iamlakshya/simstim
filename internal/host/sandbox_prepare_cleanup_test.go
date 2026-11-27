package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/paths"
)

func newPrepareCleanupReservationV1(t *testing.T) (*SessionOperation, *sessionReservation) {
	t.Helper()
	reservation, err := reserveSessionID("")
	if err != nil {
		t.Fatal(err)
	}
	operation, err := beginReservedSessionOperation(context.Background(), reservation)
	if err != nil {
		reservation.close()
		t.Fatal(err)
	}
	t.Cleanup(func() { operation.Close(); reservation.close() })
	return operation, reservation
}

func TestSandboxPrepareCleanupPreservesReplacedOriginalName(t *testing.T) {
	setupIsolatedPrepareHost(t)
	operation, reservation := newPrepareCleanupReservationV1(t)
	retained := reservation.id + "-retained"
	if err := reservation.parent.Rename(reservation.id, retained); err != nil {
		t.Fatal(err)
	}
	if err := reservation.parent.Mkdir(reservation.id, 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(paths.SessionDir(reservation.id), "keep")
	if err := os.WriteFile(foreign, []byte("unrelated replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupSandboxPrepareSessionV1(operation, reservation); err == nil {
		t.Fatal("cleanup accepted a same-name replacement")
	}
	if body, err := os.ReadFile(foreign); err != nil || string(body) != "unrelated replacement" {
		t.Fatalf("replacement changed: %q, %v", body, err)
	}
	if _, err := reservation.parent.Lstat(retained); err != nil {
		t.Fatalf("original directory was removed: %v", err)
	}
}

func TestSandboxPrepareCleanupKeepsNewPublicNameAfterAtomicClaim(t *testing.T) {
	setupIsolatedPrepareHost(t)
	operation, reservation := newPrepareCleanupReservationV1(t)
	owner := &sandboxPrepareCleanupV1{operation: operation, reservation: reservation}
	if err := owner.capture(); err != nil {
		t.Fatal(err)
	}
	operation.sandboxPrepareCleanup = owner
	if err := reservation.parent.Mkdir(reservation.id, 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(paths.SessionDir(reservation.id), "keep")
	if err := os.WriteFile(foreign, []byte("new reservation"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupSandboxPrepareSessionV1(operation, reservation); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(foreign); err != nil || string(body) != "new reservation" {
		t.Fatalf("cleanup touched the new public name: %q, %v", body, err)
	}
	if _, err := os.Lstat(filepath.Join(paths.SessionsDir(), owner.claim)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("claimed original directory remains: %v", err)
	}
}

func TestSandboxPrepareCleanupPreservesForeignLinkAndOutsideTarget(t *testing.T) {
	setupIsolatedPrepareHost(t)
	operation, reservation := newPrepareCleanupReservationV1(t)
	outside := filepath.Join(t.TempDir(), "keep")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reservation.dir.Symlink(outside, "unknown-link"); err != nil {
		t.Fatal(err)
	}
	if err := cleanupSandboxPrepareSessionV1(operation, reservation); err == nil {
		t.Fatal("cleanup accepted an unknown link")
	}
	if body, err := os.ReadFile(outside); err != nil || string(body) != "outside" {
		t.Fatalf("outside target changed: %q, %v", body, err)
	}
	if operation.native == nil || !operation.active.Load() || operation.sandboxPrepareCleanup.claim == "" {
		t.Fatal("failure dropped the original OP or anchored directory claim")
	}
}
