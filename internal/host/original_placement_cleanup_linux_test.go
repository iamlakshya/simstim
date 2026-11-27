//go:build linux

package host

import (
	"context"
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/publicationlease"
)

func TestOriginalPlacementCleanupRetainsActualPendingAcrossEntryChangeV1(t *testing.T) {
	receipt, loan, _, _ := agentsFailedCleanupFixtureV1(t)
	p, original := receipt.publication.owner, receipt.publication.operation
	_, _ = p.admitPlacementConstructorV1(t.Context(), func() (*placementOperation, error) {
		return original, errors.New("factory failed")
	})
	cleanup := &BootPlacementCleanupV1{placement: p}
	// The private fixture stands in for the actual issuance relation tested by
	// native startup tests. Cleanup must never consult a later process entry.
	placementProcesses.Lock()
	previous := placementProcesses.entry
	replacement := &placement{}
	placementProcesses.entry = replacement
	placementProcesses.Unlock()
	t.Cleanup(func() {
		placementProcesses.Lock()
		placementProcesses.entry = previous
		placementProcesses.Unlock()
	})
	if err := cleanup.CloseContextV1(t.Context()); !errors.Is(err, publicationlease.ErrBusy) {
		t.Fatal("live original fence did not retain failed cleanup", err)
	}
	if p.publicationCleanup.pending != original || replacement.publicationCleanup.pending != nil {
		t.Fatal("cleanup changed the original owner or touched another placement")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := cleanup.CloseContextV1(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cleanup ignored cancellation", err)
	}
	if p.publicationCleanup.pending != original {
		t.Fatal("cancellation consumed the original cleanup")
	}
	loan.Close()
	if err := cleanup.CloseContextV1(t.Context()); err != nil {
		t.Fatal(err)
	}
	if p.publicationCleanup.pending != nil {
		t.Fatal("successful original cleanup left pending ownership")
	}
}

func TestOriginalPlacementCleanupCannotBeIssuedFromZeroOwnersV1(t *testing.T) {
	var owner *OwnedSupervisorLease
	if _, err := owner.PlacementCleanupV1(&SessionOperation{}); err == nil {
		t.Fatal("nil native owner issued cleanup")
	}
	if _, err := (&OwnedSupervisorLease{}).PlacementCleanupV1(&SessionOperation{}); err == nil {
		t.Fatal("unaccepted operation issued cleanup")
	}
	if err := (&BootPlacementCleanupV1{}).CloseContextV1(t.Context()); err == nil {
		t.Fatal("zero cleanup handle reported success")
	}
}
