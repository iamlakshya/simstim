package client

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSessionMachineCloseRetainsOriginalBridgeAndLeaseWaiters(t *testing.T) {
	owner, _ := beginSessionMachineFixtureV1(t, false)
	logicalDone := make(chan struct{})
	close(logicalDone)
	conn := &Conn{route: route{machineOwner: owner}, stdin: &sessionMachineRootInputFixtureV1{closed: make(chan struct{})}, done: logicalDone, err: ErrClosed, procDone: make(chan struct{}), machineLeaseDone: make(chan struct{})}
	var processOnce, leaseOnce sync.Once
	finishProcess := func() { processOnce.Do(func() { close(conn.procDone) }) }
	finishLease := func() { leaseOnce.Do(func() { close(conn.machineLeaseDone) }) }
	var joined chan struct{}
	t.Cleanup(func() {
		finishProcess()
		finishLease()
		if joined != nil {
			select {
			case <-joined:
			case <-time.After(time.Second):
				t.Error("the owned join worker did not finish")
			}
		}
	})
	owner.retainConnectionV1(conn)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	err := owner.joinConnectionsV1(cancelled)
	var userErr *UserError
	if !errors.As(err, &userErr) || userErr.Code != "machine_active" || len(owner.connections) != 1 {
		t.Fatalf("logical close released the original waiter: %v", err)
	}
	finishProcess()
	finished := make(chan error, 1)
	joined = make(chan struct{})
	go func() { defer close(joined); finished <- owner.joinConnectionsV1(t.Context()) }()
	select {
	case err := <-finished:
		t.Fatalf("physical bridge exit replaced the lease worker's completion: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	finishLease()
	select {
	case err := <-finished:
		if err != nil || len(owner.connections) != 0 {
			t.Fatalf("original owners did not join: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("the original lease worker was not joined")
	}
}
