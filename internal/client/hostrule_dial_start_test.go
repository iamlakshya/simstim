package client

import (
	"context"
	"errors"
	"testing"
)

type delayedOwnedStartTransportV1 struct {
	startupTransport
	entered chan struct{}
	release <-chan struct{}
	failure error
}

func (t delayedOwnedStartTransportV1) Start(context.Context, string) (Proc, error) {
	if t.entered != nil {
		close(t.entered)
	}
	if t.release != nil {
		<-t.release
	}
	return t.proc, t.failure
}

func finishOwnedTestDialV1(owner *ownedNewDialV1, err error) {
	owner.mu.Lock()
	owner.finished, owner.dialErr = true, err
	owner.mu.Unlock()
}

func TestHostAttemptLateStartRetainsItsActualProcess(t *testing.T) {
	process := newStartupProc(t, nil, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	owner := &ownedNewDialV1{}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), ownedNewDialKeyV1{}, owner))
	done := make(chan error, 1)
	go func() {
		_, err := dial(ctx, Host{Name: "box"}, route{t: delayedOwnedStartTransportV1{
			startupTransport: startupTransport{proc: process}, entered: entered, release: release}})
		finishOwnedTestDialV1(owner, err)
		done <- err
	}()
	startupSignal(t, entered, "the actual late native Start")
	cancel()
	if err := startupError(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	owner.mu.Lock()
	captured := len(owner.conns)
	owner.mu.Unlock()
	if captured != 0 {
		t.Fatal("late Start unexpectedly allocated a Conn")
	}
	bounded, stop := context.WithCancel(t.Context())
	stop()
	if complete, err := owner.settle(bounded, nil); complete || err == nil {
		t.Fatal("empty connection capture manufactured process absence", complete, err)
	}
	close(release)
	startupSignal(t, process.waiting, "the late process's sole native waiter")
	if complete, err := owner.settle(t.Context(), nil); !complete || err != nil {
		t.Fatal(complete, err)
	}
	if process.waitCalls.Load() != 1 || owner.retryableUnreachableV1() {
		t.Fatal("late cancellation repeated Wait or authorised retry")
	}
}

func TestHostAttemptRetryUsesTheFinalNativeStart(t *testing.T) {
	longStartupGrace(t)
	process := newStartupProc(t, nil, ErrUnreachable)
	process.stderr = "the controlled transport could not connect"
	process.end()
	owner := &ownedNewDialV1{}
	ctx := context.WithValue(t.Context(), ownedNewDialKeyV1{}, owner)
	_, err := dial(ctx, Host{Name: "box"}, route{t: startupTransport{proc: process}})
	finishOwnedTestDialV1(owner, err)
	if complete, settleErr := owner.settle(t.Context(), nil); !complete || settleErr != nil {
		t.Fatal(complete, settleErr)
	}
	if !owner.retryableUnreachableV1() {
		t.Fatal("the exact joined unreachable process was lost", err)
	}
	// A later native Start can fail before any new Conn exists. The earlier
	// process's diagnostic cannot stand in for this final actual result.
	owner.mu.Lock()
	owner.finished = false
	owner.mu.Unlock()
	_, err = dial(ctx, Host{Name: "box"}, route{t: delayedOwnedStartTransportV1{failure: ErrNotInstalled}})
	finishOwnedTestDialV1(owner, err)
	if complete, settleErr := owner.settle(t.Context(), nil); !complete || settleErr != nil {
		t.Fatal(complete, settleErr)
	}
	if owner.retryableUnreachableV1() {
		t.Fatal("an older Conn became proof of the final native Start")
	}
	if process.waitCalls.Load() != 1 {
		t.Fatal("native Wait repeated")
	}
}

func TestHostAttemptNativeStartErrorRequiresItsActualReturn(t *testing.T) {
	owner := &ownedNewDialV1{}
	ctx := context.WithValue(t.Context(), ownedNewDialKeyV1{}, owner)
	_, err := dial(ctx, Host{Name: "box"}, route{t: delayedOwnedStartTransportV1{failure: ErrUnreachable}})
	finishOwnedTestDialV1(owner, err)
	if complete, settleErr := owner.settle(t.Context(), nil); !complete || settleErr != nil {
		t.Fatal(complete, settleErr)
	}
	if !owner.retryableUnreachableV1() {
		t.Fatal("actual native Start refusal was lost", err)
	}
	owner.mu.Lock()
	owner.dialErr = &UserError{Code: "unreachable", Msg: "another failure", Hint: "Try again."}
	owner.mu.Unlock()
	if owner.retryableUnreachableV1() {
		t.Fatal("a donated matching error code authorised retry")
	}
}
