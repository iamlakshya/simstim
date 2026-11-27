package client

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestHostAttemptStartupWaitHasOneActualOwner(t *testing.T) {
	longStartupGrace(t)
	exit := errors.New("the actual owned transport exit")
	process := newStartupProc(t, nil, exit)
	conn := &Conn{proc: process, stdin: process.Stdin(), done: make(chan struct{}), procDone: make(chan struct{})}
	result := make(chan error, 1)
	go func() { result <- conn.waitStartup(t.Context()) }()
	startupSignal(t, process.waiting, "the single actual waiter")
	conn.watchProcess()
	process.end()
	if err := startupError(t, result); !errors.Is(err, exit) {
		t.Fatal("startup lost the actual wait result", err)
	}
	startupSignal(t, conn.procDone, "the native wait join")
	if process.waitCalls.Load() != 1 || !errors.Is(conn.processWaitErrorV1(), exit) {
		t.Fatal("startup and steady-state consumers started separate waiters")
	}
}

func TestHostAttemptFailedDialRetainsAndJoinsItsActualConnection(t *testing.T) {
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
	ctx := context.WithValue(t.Context(), ownedNewDialKeyV1{}, owner)
	conn, err := dial(ctx, Host{Name: "box"}, route{t: startupTransport{proc: process}})
	if err == nil || conn != nil {
		t.Fatal("a bad hello unexpectedly became a connection")
	}
	owner.mu.Lock()
	owner.finished, owner.dialErr = true, err // the actual call above has returned
	count := len(owner.conns)
	owner.mu.Unlock()
	if count != 1 {
		t.Fatal("the failed dial lost its actual allocated native owner", count)
	}
	startupSignal(t, process.waiting, "the failed owner's one actual waiter")
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if settled, err := owner.settle(cancelled, nil); settled || err == nil {
		t.Fatal("cancellation was accepted as physical process exit", settled, err)
	}
	owner.mu.Lock()
	count = len(owner.conns)
	actual := owner.conns[0]
	owner.mu.Unlock()
	if count != 1 {
		t.Fatal("uncertain cleanup discarded the actual owner")
	}
	// Release the same process's controlled wait. A later join may then
	// complete; it does not mint absence from the earlier timeout.
	// afterKill owns an explicit delayed finalizer, so release it now.
	releaseOnce()
	if settled, err := owner.settle(t.Context(), nil); !settled || err != nil {
		t.Fatal("the original process could not settle after its actual wait", settled, err)
	}
	startupSignal(t, actual.procDone, "the retained failed owner's wait")
	if process.waitCalls.Load() != 1 || owner.waitError() == nil {
		t.Fatal("native wait diagnostics were lost or the waiter was repeated")
	}
}

func TestHostAttemptForeignKeepCannotRetireAnotherConnection(t *testing.T) {
	owner := &ownedNewDialV1{finished: true}
	if settled, err := owner.settle(t.Context(), &Conn{}); settled || err == nil {
		t.Fatal("a caller row selected another connection as the retained owner")
	}
}
