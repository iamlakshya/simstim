package tui

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestGrantCleanupCancelsAndJoinsItsActualOwnedAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	request := client.NewGrantRequest(false, false)
	var owners grantOwners
	end, err := owners.begin(request, ctx, cancel)
	if err != nil {
		t.Fatal(err)
	}
	joined := make(chan struct{})
	allowJoin := make(chan struct{})
	var release sync.Once
	go func() { <-ctx.Done(); <-allowJoin; end(); close(joined) }()
	cleaned := make(chan error, 1)
	cleanupFinished := make(chan struct{})
	go func() { defer close(cleanupFinished); cleaned <- owners.release(t.Context(), request) }()
	t.Cleanup(func() {
		cancel()
		release.Do(func() { close(allowJoin) })
		select {
		case <-joined:
		case <-time.After(5 * time.Second):
			t.Error("owned attempt remained after test cleanup")
		}
		select {
		case <-cleanupFinished:
		case <-time.After(5 * time.Second):
			t.Error("owned request cleanup remained after test cleanup")
		}
	})
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not cancel its original attempt")
	}
	select {
	case err := <-cleaned:
		t.Fatalf("cleanup passed an unjoined attempt: %v", err)
	default:
	}
	// Complete the same actual attempt; no fresh request or join is substituted.
	release.Do(func() { close(allowJoin) })
	select {
	case <-joined:
	case <-time.After(5 * time.Second):
		t.Fatal("the canceled attempt did not join")
	}
	select {
	case err := <-cleaned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("joined cleanup did not finish")
	}
}

func TestGrantCleanupRetainsTheOriginalAbsoluteDeadline(t *testing.T) {
	deadline := time.Now().Add(time.Minute)
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	cancel()
	cleanup, finish := grantCleanupContext(ctx)
	defer finish()
	got, ok := cleanup.Deadline()
	if !ok || !got.Equal(deadline) || cleanup.Err() != nil {
		t.Fatalf("cleanup reset or lost the original budget: deadline=%v err=%v", got, cleanup.Err())
	}
}

func TestGrantCleanupRejectsAnUnboundedActiveAttempt(t *testing.T) {
	var owners grantOwners
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	defer cancel()
	if _, err := owners.begin(client.NewGrantRequest(false, false), ctx, cancel); err == nil {
		t.Fatal("an active start gained an unbounded cleanup budget")
	}
}
