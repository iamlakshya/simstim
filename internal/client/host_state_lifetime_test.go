package client

import (
	"context"
	"errors"
	"testing"
	"time"
)

func hostStateLifetimeAwait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	case <-time.After(5 * time.Second):
		t.Fatal("the owned lifetime did not finish")
	}
}

func hostStateLifetimeStaysBlocked(t *testing.T, done <-chan struct{}) {
	t.Helper()
	// This scheduling window is a negative ordering witness. Positive
	// completion is separately joined after the owned user releases below.
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-done:
		t.Fatal("the owner passed an active lifetime user")
	case <-timer.C:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
}

func TestHostStateLifetimeCloseDrainsExistingTransactionBeforePhysicalClose(t *testing.T) {
	stateFDTestHome(t)
	life := newHostStateLifetime()
	release, err := life.enter(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, stop := life.transactionContext(context.Background())
	defer stop()
	life.beginClose()
	if _, err := life.enter(t.Context()); err == nil {
		t.Fatal("an ended operation admitted a new transaction")
	}
	hostStateLifetimeAwait(t, ctx.Done())
	physicalClosed := make(chan struct{})
	go func() { life.drain(); close(physicalClosed) }()
	hostStateLifetimeStaysBlocked(t, physicalClosed)
	release()
	release()
	hostStateLifetimeAwait(t, physicalClosed)
}

func TestHostStateLifetimeRetirementBlocksLaterUsersAndDrainsEarlierUsers(t *testing.T) {
	stateFDTestHome(t)
	life := newHostStateLifetime()
	defer life.beginClose()
	release, err := life.enter(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	retired := make(chan struct{})
	resume := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		end, err := life.retire(t.Context())
		if err != nil {
			t.Error(err)
			return
		}
		defer end()
		close(retired)
		<-resume
	}()
	for {
		life.mu.Lock()
		claimed := life.retiring
		changed := life.changed
		life.mu.Unlock()
		if claimed {
			break
		}
		hostStateLifetimeAwait(t, changed)
	}
	hostStateLifetimeStaysBlocked(t, retired)
	later, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := life.enter(later); !errors.Is(err, context.Canceled) {
		t.Fatalf("a later user bypassed retirement or cancellation: %v", err)
	}
	release()
	hostStateLifetimeAwait(t, retired)
	blocked, cancelBlocked := context.WithCancel(t.Context())
	defer cancelBlocked()
	entered := make(chan struct{})
	laterFinished := make(chan struct{})
	go func() {
		defer close(laterFinished)
		end, err := life.enter(blocked)
		if err != nil {
			t.Error(err)
			return
		}
		defer end()
		close(entered)
	}()
	hostStateLifetimeStaysBlocked(t, entered)
	close(resume)
	hostStateLifetimeAwait(t, finished)
	hostStateLifetimeAwait(t, entered)
	hostStateLifetimeAwait(t, laterFinished)
}

func TestHostStateLifetimeCloseAlsoWaitsForActiveFinaliser(t *testing.T) {
	stateFDTestHome(t)
	life := newHostStateLifetime()
	end, err := life.retire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer end()
	life.beginClose()
	closed := make(chan struct{})
	go func() { life.drain(); close(closed) }()
	hostStateLifetimeStaysBlocked(t, closed)
	end()
	end()
	hostStateLifetimeAwait(t, closed)
}

func TestHostStateLifetimeCancelledRetirementRestoresAdmission(t *testing.T) {
	stateFDTestHome(t)
	life := newHostStateLifetime()
	defer life.beginClose()
	release, err := life.enter(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		if _, err := life.retire(ctx); !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled retirement: %v", err)
		}
	}()
	for {
		life.mu.Lock()
		claimed := life.retiring
		changed := life.changed
		life.mu.Unlock()
		if claimed {
			break
		}
		hostStateLifetimeAwait(t, changed)
	}
	cancel()
	hostStateLifetimeAwait(t, ended)
	second, err := life.enter(t.Context())
	if err != nil {
		t.Fatal("cancelled retirement left admission closed:", err)
	}
	second()
	release()
}

func TestHostStateLifetimeContextPreservesParentCancellation(t *testing.T) {
	stateFDTestHome(t)
	life := newHostStateLifetime()
	defer life.beginClose()
	parent, cancel := context.WithCancel(t.Context())
	ctx, stop := life.transactionContext(parent)
	defer stop()
	cancel()
	hostStateLifetimeAwait(t, ctx.Done())
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("parent cancellation identity changed: %v", ctx.Err())
	}
}
