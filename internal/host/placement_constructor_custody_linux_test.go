//go:build linux

package host

import (
	"context"
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/publicationlease"
)

func TestPlacementFailedConstructorRetainsOneActualObjectBeforeNewAdmissionV1(t *testing.T) {
	receipt, loan, root, token := agentsFailedCleanupFixtureV1(t)
	p, original := receipt.publication.owner, receipt.publication.operation
	failure := errors.New("original constructor failed")
	calls := 0
	returned, err := p.admitPlacementConstructorV1(t.Context(), func() (*placementOperation, error) {
		calls++
		return original, failure
	})
	if returned != nil || !errors.Is(err, failure) || !errors.Is(err, publicationlease.ErrBusy) {
		t.Fatal("constructor lost original cleanup error", err)
	}
	if p.publicationCleanup.pending != original {
		t.Fatal("failed constructor lost its actual original object")
	}
	if _, err := p.admitPlacementConstructorV1(t.Context(), func() (*placementOperation, error) { calls++; return nil, nil }); !errors.Is(err, publicationlease.ErrBusy) {
		t.Fatal("new constructor bypassed original cleanup", err)
	}
	if calls != 1 || p.publicationCleanup.pending != original {
		t.Fatal("unresolved cleanup grew constructor retention or admitted new work")
	}
	control, found, err := readPlacementControl(root, p.scope)
	if err != nil || !found || len(control.Active) != 1 || control.Active[token].Fence == "" {
		t.Fatal("failed admission replaced the original Active operation", err)
	}
	loan.Close()
	if _, err := p.admitPlacementConstructorV1(t.Context(), func() (*placementOperation, error) { calls++; return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || p.publicationCleanup.pending != nil {
		t.Fatal("successful exact cleanup did not clear the original failure")
	}
	control, _, err = readPlacementControl(root, p.scope)
	if err != nil || len(control.Active) != 0 {
		t.Fatal("retry failed to retire original native operation", err)
	}
}

func TestPlacementConstructorGateCancellationNeverCreatesAnotherSourceV1(t *testing.T) {
	receipt, loan, _, _ := agentsFailedCleanupFixtureV1(t)
	p := receipt.publication.owner
	unlock, err := p.publicationCleanup.lockV1(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	started, done := make(chan struct{}), make(chan error, 1)
	called := false
	go func() {
		close(started)
		_, err := p.admitPlacementConstructorV1(ctx, func() (*placementOperation, error) { called = true; return nil, nil })
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("blocked admission ignored original cancellation", err)
	}
	unlock()
	loan.Close()
	if called || p.publicationCleanup.pending != nil {
		t.Fatal("cancelled waiter constructed or retained another source")
	}
}

func TestPlacementCancelledRetryPreservesSameActualCleanupV1(t *testing.T) {
	receipt, loan, _, _ := agentsFailedCleanupFixtureV1(t)
	p, original := receipt.publication.owner, receipt.publication.operation
	_, _ = p.admitPlacementConstructorV1(t.Context(), func() (*placementOperation, error) { return original, errors.New("constructor stopped") })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := p.retryPlacementConstructorV1(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("retry ignored cancellation", err)
	}
	if p.publicationCleanup.pending != original {
		t.Fatal("cancelled retry dropped the original native source")
	}
	loan.Close()
	if err := p.retryPlacementConstructorV1(t.Context()); err != nil {
		t.Fatal(err)
	}
	if p.publicationCleanup.pending != nil {
		t.Fatal("successful cleanup did not release exact retained object")
	}
}
