package client

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// This generated process exercises the owned Wait/Kill join only. It starts
// no native process and proves no kernel latency or actual root behaviour.
type rootStepWaitFixture struct {
	waiting, killed, release, finished chan struct{}
	err                                error
}

func (p *rootStepWaitFixture) Wait() error {
	close(p.waiting)
	defer close(p.finished)
	<-p.release
	return p.err
}
func (p *rootStepWaitFixture) Kill() { close(p.killed) }

func TestRootStepCancellationKeepsOwnedWaitAndNativeObservation(t *testing.T) {
	observation := errors.New("generated native wait observation")
	process := &rootStepWaitFixture{waiting: make(chan struct{}), killed: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{}), err: observation}
	var once sync.Once
	release := func() { once.Do(func() { close(process.release) }) }
	t.Cleanup(func() {
		release()
		select {
		case <-process.waiting:
			<-process.finished
		default:
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		waitErr := waitRootStepProcess(ctx, process)
		result <- rootStepApplyResult(ctx, waitErr, false)
	}()
	select {
	case <-process.waiting:
	case <-time.After(2 * time.Second):
		t.Fatal("generated process handshake did not complete")
	}
	cancel()
	select {
	case <-process.killed:
	case <-time.After(2 * time.Second):
		t.Fatal("generated process handshake did not complete")
	}
	select {
	case err := <-result:
		t.Fatalf("cancellation fabricated owned native exit: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	release()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, observation) {
			t.Fatalf("the joined cancellation lost its original wait observation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("generated process handshake did not complete")
	}
}

func TestRootApplyCancelledAfterPhysicalWaitKeepsBothErrors(t *testing.T) {
	observation := errors.New("completed native wait observation")
	process := &rootStepWaitFixture{waiting: make(chan struct{}), killed: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{}), err: observation}
	// The process fixture exists and can complete before cancellation. No
	// command, network endpoint or production constructor is replaced.
	close(process.release)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	waitErr := waitRootStepProcess(ctx, process)
	if !errors.Is(waitErr, observation) {
		t.Fatal("the physical Wait observation was lost before result handling", waitErr)
	}
	select {
	case <-process.finished:
	default:
		t.Fatal("the original Wait was not joined")
	}
	cancel()
	// Exercise the same production apply result path with a valid marker.
	// A marker cannot turn this later cancellation into success or erase Wait.
	err := rootStepApplyResult(ctx, waitErr, true)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, observation) {
		t.Fatalf("apply cancellation discarded the completed wait error: %v", err)
	}
}

func TestRootHistoryRejectsUnownedOrPrematureCompletionFacts(t *testing.T) {
	for _, test := range []struct {
		name, status                                        string
		proof, attempted, starting, done, verified, allowed bool
	}{
		{"bare running data", "running", false, true, true, false, false, false},
		{"no owned start", "running", true, false, false, false, false, false},
		{"owned start", "running", true, true, true, false, false, true},
		{"premature confirmation", "confirmed", true, true, true, false, false, false},
		{"failed attempt", "failed", true, true, false, true, false, true},
		{"unverified confirmation", "confirmed", true, true, false, true, false, false},
		{"verified result marked failed", "failed", true, true, false, true, true, false},
		{"verified completion", "confirmed", true, true, false, true, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := h5GrantState(t)
			conn := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id")
			script, err := BuildRootScript("fixture", 1000, []RootStepID{RootLinger}, 0)
			if err != nil {
				t.Fatal(err)
			}
			request := NewRootStepGrantRequest()
			ctx := WithGrantRequest(t.Context(), request)
			if err := requireLocalOperation(ctx, grantLocalRoot, grantOpRootStep); err != nil {
				t.Fatal(err)
			}
			intent := &rootStepIntent{conn: conn, script: script, mode: RootRun}
			execution := &rootStepExecution{intent: intent, request: request, attempted: test.attempted, starting: test.starting, done: test.done, verified: test.verified}
			request.root, request.rootExecution = intent, execution
			if test.proof {
				ctx = context.WithValue(ctx, rootStepRecordProofKey{}, execution)
			}
			// This checks the phase guard only. Generated private facts are
			// never passed to the real writer, native command or Source gate.
			record := RootStepRecord{HostID: conn.HostID(), Steps: []RootStepID{RootLinger}, SHA256: script.SHA256, Mode: RootRun, At: time.Unix(1, 0).UTC(), Status: test.status}
			if err := checkRootStepRecordPurpose(ctx, h, record); (err == nil) != test.allowed {
				t.Fatalf("history phase acceptance=%v, want %v: %v", err == nil, test.allowed, err)
			}
			execution.starting = false
			if err := request.Close(); err != nil {
				t.Fatal(err)
			}
			if err := checkRootStepRecordPurpose(ctx, h, record); err == nil {
				t.Fatal("closed original request renewed its history admission")
			}
		})
	}
}
