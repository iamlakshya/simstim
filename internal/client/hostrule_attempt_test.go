package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

// These lower-owner tests deliberately do not mint a successful selection,
// native admission or remote absence. They exercise rejection, original-context
// ownership and actual delayed Proc.Wait joining without contacting a host.
func attemptResultFixtureV1(t *testing.T) (*HostAttemptPlanV1, *HostAttemptV1) {
	t.Helper()
	owner, end := context.WithCancel(t.Context())
	t.Cleanup(end)
	p := &HostAttemptPlanV1{ctx: owner, cancel: end, names: []string{"box", "other"}, next: 1}
	work, cancel := context.WithCancel(owner)
	r := NewGrantRequest(false, false)
	a := &HostAttemptV1{plan: p, ctx: WithGrantRequest(work, r), cancel: cancel, request: r}
	p.active = a
	return p, a
}

func TestHostAttemptCallerContextCannotDonateValuesOrLifetime(t *testing.T) {
	type key struct{}
	r := NewGrantRequest(false, false)
	original, cancel := context.WithCancel(WithGrantRequest(context.WithValue(t.Context(), key{}, "original"), r))
	defer cancel()
	caller := context.WithValue(context.WithValue(t.Context(), key{}, "replacement"), grantRequestKey{}, NewGrantRequest(true, true))
	call, end, err := narrowHostAttemptContextV1(original, caller)
	if err != nil {
		t.Fatal(err)
	}
	defer end()
	if call.Value(key{}) != "original" || grantRequestOf(call) != r {
		t.Fatal("caller values replaced the original bound request")
	}
	cancel()
	if !errors.Is(call.Err(), context.Canceled) {
		t.Fatal("caller context extended the original request lifetime")
	}
}

func TestHostAttemptRetryRequiresItsExactReturnedResult(t *testing.T) {
	for _, kind := range []string{"public code", "wrapped", "copied", "other attempt", "stale"} {
		t.Run(kind, func(t *testing.T) {
			p, a := attemptResultFixtureV1(t)
			actual := &UserError{Msg: "cannot reach the original host", Hint: "Check the original transport.", Code: "unreachable"}
			returned := a.recordFailureV1(actual, hostAttemptDialV1)
			var supplied error
			switch kind {
			case "public code":
				supplied = &UserError{Msg: actual.Msg, Hint: actual.Hint, Code: actual.Code}
			case "wrapped":
				supplied = fmt.Errorf("caller wrapper: %w", returned)
			case "copied":
				copy := *returned.(*hostAttemptFailureV1)
				supplied = &copy
			case "other attempt":
				_, other := attemptResultFixtureV1(t)
				supplied = other.recordFailureV1(actual, hostAttemptDialV1)
			case "stale":
				supplied = returned
				a.recordFailureV1(errors.New("a newer actual result"), hostAttemptLocalV1)
			}
			if retry, err := p.Retry(t.Context(), a, supplied); retry || err == nil {
				t.Fatal("a caller-created or unrelated result advanced the host list", retry, err)
			}
			if p.active != a || a.cleanup != nil || a.request.ending {
				t.Fatal("an invalid retry touched the actual active owner")
			}
		})
	}
}

func TestHostAttemptFailureClassificationDoesNotFollowPublicMutation(t *testing.T) {
	_, a := attemptResultFixtureV1(t)
	actual := &UserError{Msg: "an actual refusal", Hint: "Start another request.", Code: "secret_not_here", Kind: KindRefused}
	returned := a.recordFailureV1(actual, hostAttemptDialV1).(*hostAttemptFailureV1)
	actual.Code, actual.Kind = "unreachable", KindFailed
	if returned.kind != hostAttemptNoRetryV1 || !errors.Is(returned, actual) {
		t.Fatal("mutable public metadata changed private classification or lost Unwrap")
	}
}

func TestHostAttemptEOFAndTimerAreNeverReachabilityEvidence(t *testing.T) {
	for _, actual := range []error{io.EOF, io.ErrClosedPipe, context.Canceled, context.DeadlineExceeded} {
		p, a := attemptResultFixtureV1(t)
		returned := a.recordFailureV1(actual, hostAttemptDialV1)
		if retry, err := p.Retry(t.Context(), a, returned); retry || err != nil {
			t.Fatal("a stream/timer result became native unreachable proof", actual, retry, err)
		}
		if a.cleanup != nil || p.active != a {
			t.Fatal("a stream/timer result consumed the retained owner")
		}
	}
}

func TestHostAttemptNoRoomWithoutSamePristinePlanCannotRetry(t *testing.T) {
	for _, state := range []string{"none", "attempted", "busy", "published", "other connection"} {
		t.Run(state, func(t *testing.T) {
			p, a := attemptResultFixtureV1(t)
			a.conn = &Conn{}
			if state != "none" {
				reservation := &newSecretPlan{conn: a.conn, creation: &SandboxPolicy{}}
				switch state {
				case "attempted":
					reservation.attempted = true
				case "busy":
					reservation.busy = true
				case "published":
					reservation.published = true
				case "other connection":
					reservation.conn = &Conn{}
				}
				a.request.newSecrets = reservation
			}
			returned := a.recordFailureV1(&UserError{Msg: "the host has no room", Hint: "Choose another host.", Code: "no_room"}, hostAttemptNewV1)
			if retry, err := p.Retry(t.Context(), a, returned); retry || err != nil {
				t.Fatal("no_room without the exact pre-write plan became retry permission", retry, err)
			}
			if a.cleanup != nil || p.active != a {
				t.Fatal("an attempted/uncertain creation lost its actual owner")
			}
		})
	}
}

func TestHostAttemptAbortFalseIsNotNegativeCreationProof(t *testing.T) {
	p, a := attemptResultFixtureV1(t)
	a.conn = &Conn{}
	reservation := &newSecretPlan{conn: a.conn, creation: &SandboxPolicy{}}
	a.request.newSecrets = reservation
	a.newCalled = true
	returned := a.recordFailureV1(&UserError{Msg: "the host has no room", Hint: "Check that host.", Code: "no_room"}, hostAttemptNewV1)
	// A real first writer may win before cleanup obtains the terminal lease.
	reservation.attempted = true
	if retired, err := a.request.Abort(t.Context()); retired || err != nil {
		t.Fatal("fixture failed to expose the actual Abort(false,nil) result", retired, err)
	}
	if retry, err := p.Retry(t.Context(), a, returned); retry || err == nil {
		t.Fatal("Abort(false,nil) was mistaken for absence", retry, err)
	}
	if p.active != a || a.request.newSecrets != reservation || reservation.retired {
		t.Fatal("post-write uncertainty discarded or retired the original witness")
	}
}

func TestHostAttemptCloseRetainsAnActualDelayedNativeWait(t *testing.T) {
	_, a := attemptResultFixtureV1(t)
	process := newStartupProc(t, nil, nil)
	release := make(chan struct{})
	process.afterKill = release
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()
	var frame bytes.Buffer
	if err := proto.NewWriter(&frame).Write(proto.TPing, nil); err != nil {
		t.Fatal(err)
	}
	process.out = &frame
	owner := &ownedNewDialV1{}
	ctx := context.WithValue(a.Context(), ownedNewDialKeyV1{}, owner)
	conn, actual := dial(ctx, Host{Name: "box"}, route{t: startupTransport{proc: process}})
	if actual == nil || conn != nil {
		t.Fatal("the actual bad hello unexpectedly succeeded")
	}
	owner.mu.Lock()
	owner.finished, owner.dialErr = true, actual
	if len(owner.conns) != 1 {
		owner.mu.Unlock()
		t.Fatal("the actual failed connection was not retained")
	}
	retained := owner.conns[0]
	owner.mu.Unlock()
	a.dial = owner
	a.recordFailureV1(actual, hostAttemptDialV1)
	startupSignal(t, process.waiting, "the actual process waiter")
	expired, end := context.WithDeadline(a.Context(), time.Now().Add(-time.Second))
	defer end()
	a.ctx = expired
	if err := a.Close(); err == nil {
		t.Fatal("a cleanup deadline claimed the native process had exited")
	}
	if a.dial != owner || owner.conns[0] != retained || a.closed {
		t.Fatal("an incomplete cleanup discarded its actual native owner")
	}
	releaseOnce()
	startupSignal(t, retained.procDone, "the same retained process's native Wait")
	a.mu.Lock()
	firstJob := a.cleanup
	a.mu.Unlock()
	startupSignal(t, firstJob.done, "the first bounded cleanup worker")
	// With the original deadline expired, starting a retained cleanup join
	// may return before its worker runs. Join that exact worker explicitly.
	_ = a.Close()
	a.mu.Lock()
	lastJob := a.cleanup
	a.mu.Unlock()
	startupSignal(t, lastJob.done, "the retained cleanup worker after native exit")
	if err := a.Close(); err != nil {
		t.Fatal("the original actual owner could not be joined after release", err)
	}
	if process.waitCalls.Load() != 1 || !a.closed || owner.waitError() == nil {
		t.Fatal("cleanup lost the native result or invoked another Wait")
	}
}

func TestHostAttemptSuccessfulCleanupClosesClientOwnersWithoutAbort(t *testing.T) {
	_, a := attemptResultFixtureV1(t)
	process := newStartupProc(t, nil, nil)
	conn := &Conn{Host: Host{Name: "box"}, proc: process, stdin: process.Stdin(),
		done: make(chan struct{}), procDone: make(chan struct{})}
	conn.watchProcess()
	process.end()
	startupSignal(t, conn.procDone, "the successful attempt's native transport exit")
	reservation := &newSecretPlan{conn: conn, creation: &SandboxPolicy{}}
	a.request.newSecrets = reservation
	a.conn, a.started = conn, true
	a.dial = &ownedNewDialV1{conns: []*Conn{conn}, main: conn, finished: true}
	// This lower lifecycle fixture deliberately supplies no valid retirement
	// snapshot: invoking Abort would fail. Successful closure must skip it.
	if err := a.Close(); err != nil {
		t.Fatal("successful session closure attempted to retire its reservation", err)
	}
	if reservation.retired || !reservation.closed || process.waitCalls.Load() != 1 {
		t.Fatal("successful cleanup retired remote state or did not join client owners")
	}
}
