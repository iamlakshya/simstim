package client

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestHostTerminalVerifiedScopeCancelsAndJoinsEarlierNestedWorker(t *testing.T) {
	h, ctx, _, _, _ := hostTerminalFixture(t)
	entered, ended := make(chan struct{}), make(chan struct{})
	workerErr := make(chan error, 1)
	err := WithHostForgetCommittedContext(ctx, h, func(verified context.Context) error {
		go func() {
			defer close(ended)
			workerErr <- withStateFileFD(verified, []string{"hosts", HostFileKey(h.Name)}, "baseline.json", false, func(stateFileView) (stateFileReplace, error) {
				close(entered)
				<-verified.Done()
				return nil, verified.Err()
			})
		}()
		hostStateLifetimeAwait(t, entered)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The outer scope cannot return until this actual transaction has exited
	// its callback, revoked its reader and released its native handles.
	hostStateLifetimeAwait(t, ended)
	if err := <-workerErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("nested worker did not retain scope cancellation identity: %v", err)
	}
}

func TestHostTerminalQueuedInventoryRefusesLaterStateAcquisition(t *testing.T) {
	h, ctx, _, _, validators := hostTerminalFixture(t)
	var local HostStateRemovalResult
	err := WithHostForgetCommittedContext(ctx, h, func(verified context.Context) error {
		var err error
		local, err = RemoveHostStateContext(verified, h, strings.Repeat("a", 32), validators)
		if err != nil {
			return err
		}
		called := false
		err = withStateFileFD(verified, []string{"hosts", HostFileKey(h.Name)}, "baseline.json", false, func(stateFileView) (stateFileReplace, error) {
			called = true
			return nil, nil
		})
		if err == nil || called {
			t.Fatal("a queued immutable checkpoint admitted later state work")
		}
		return nil
	})
	if err != nil || len(local.FinalResult().Retained) != 0 {
		t.Fatalf("sealed inventory failed to finish: %+v, %v", local.FinalResult(), err)
	}
}

func TestHostTerminalOldUnlockCannotDeleteNewDiagnosticAfterLastUnlink(t *testing.T) {
	h, ctx, oldUnlock, _, validators := hostTerminalFixture(t)
	if _, err := RemoveHostStateContext(ctx, h, strings.Repeat("a", 32), validators); err != nil {
		t.Fatal(err)
	}
	if err := SaveHostContext(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	_, release, err := LockHost(t.Context(), h.Name, "new operation after actual retirement")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	who := hostLockPath(h.Name) + ".who"
	before, err := os.Stat(who)
	if err != nil {
		t.Fatal(err)
	}
	oldUnlock()
	oldUnlock()
	after, err := os.Stat(who)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("old deferred unlock deleted a new diagnostic: %v", err)
	}
}

func TestHostTerminalInspectorCannotEscapeExistingSourceViewOrBorrowInspection(t *testing.T) {
	h, _, unlock, inspect, _ := hostTerminalFixture(t)
	unlock()
	var escaped io.ReaderAt
	wrapper := func(ctx context.Context, reader io.ReaderAt, size int64, name string) (HostStateReceiptIdentity, error) {
		escaped = reader
		called := false
		err := withStateFile(ctx, []string{"hosts", HostFileKey(name)}, "removal.json", false, func([]byte, bool) ([]byte, error) {
			called = true
			return nil, nil
		})
		if err == nil || called {
			t.Fatal("the pure owner callback borrowed the private inspection acquisition role")
		}
		return inspect(ctx, reader, size, name)
	}
	found, err := WithHostStateRetirementContext(t.Context(), h.Name, wrapper, func(context.Context, HostStateReceiptIdentity) error { return nil })
	if err != nil || !found {
		t.Fatalf("existing source inspection failed: %v", err)
	}
	if _, err := escaped.ReadAt(make([]byte, 1), 0); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("pure owner inspection view escaped its canonical transaction: %v", err)
	}
}
