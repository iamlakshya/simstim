package client

import (
	"context"
	"errors"
	"testing"
)

func terminalCreationFixture(t *testing.T) (context.Context, Host) {
	t.Helper()
	stateFDTestHome(t)
	req := hostCreationRequestFixture()
	ctx, release, err := BeginHostCreationContext(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	hostCreationPreflightEmpty(t, ctx, req.Name)
	if err := StartHostCreationContext(ctx); err != nil {
		t.Fatal(err)
	}
	bound, err := BindHostCreationResultContext(ctx, hostCreationResultFixture(req))
	if err != nil {
		t.Fatal(err)
	}
	return ctx, bound
}

func TestCreationResultBindsOnceAndTerminalEndSurvivesCancellation(t *testing.T) {
	ctx, bound := terminalCreationFixture(t)
	if _, err := BindHostCreationResultContext(ctx, bound); err == nil {
		t.Fatal("same result rebound a second creation event")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	EndHostCreationAttemptContext(cancelled)
	fresh := context.WithoutCancel(ctx)
	if _, err := BindHostCreationResultContext(fresh, bound); err == nil {
		t.Fatal("cancelled terminal end left original callback replayable")
	}
	if err := UpdateHosts(fresh, HostChange{Kind: "replay", Now: &bound}); err == nil {
		t.Fatal("ended creator published under still-held outer OP")
	}
	if held := ctx.Value(heldKey{bound.Name}).(*heldLock); held.gone.Load() {
		t.Fatal("terminal phase fabricated physical OP release")
	}
}

func TestFailedCreationSaveEndsFateAfterWriterChecks(t *testing.T) {
	ctx, bound := terminalCreationFixture(t)
	marker := errors.New("actual canonical hosts write fails")
	old := writeHosts
	defer func() { writeHosts = old }()
	calls := 0
	writeHosts = func([]Host) error { calls++; return marker }
	if err := UpdateHosts(ctx, HostChange{Kind: "host create", Now: &bound}); err != marker {
		t.Fatalf("actual save failure: %v", err)
	}
	if calls != 1 {
		t.Fatal("birth was consumed before actual writer or repeated blindly")
	}
	if _, err := BindHostCreationResultContext(ctx, bound); err == nil {
		t.Fatal("failed writer allowed same-context result replay")
	}
	if err := UpdateHosts(ctx, HostChange{Kind: "retry ended", Now: &bound}); err == nil || calls != 1 {
		t.Fatal("failed creator re-entered metadata writer")
	}
}
