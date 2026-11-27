package supervisor

import (
	"net"
	"testing"
	"time"
)

// Actual pipe IO and the real admitted stack are owned here. No VM, stopped
// source, backend closure or native manager receipt is fabricated by this test.
func TestRequestShutdownClosesIOAndJoinsActualOwnerBeforeNewAdmission(t *testing.T) {
	root := &Supervisor{}
	local, remote := net.Pipe()
	defer remote.Close()
	owned, ok := root.ownRequestConnectionV1(local)
	if !ok {
		t.Fatal("initial physical request was not admitted")
	}
	readDone := make(chan struct{})
	go func() { defer close(readDone); var data [1]byte; _, _ = local.Read(data[:]) }()
	root.cancelRequestConnectionsV1()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not close actual request IO")
	}
	joined := make(chan struct{})
	go func() { root.joinRequestConnectionsV1(); close(joined) }()
	select {
	case <-joined:
		t.Fatal("shutdown accepted a closed socket as joined handler ownership")
	default:
	}
	next, nextPeer := net.Pipe()
	defer nextPeer.Close()
	if _, ok := root.ownRequestConnectionV1(next); ok {
		t.Fatal("a closing supervisor admitted another request owner")
	}
	root.finishRequestConnectionV1(owned)
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("shutdown failed to join the completed original stack")
	}
	root.finishRequestConnectionV1(owned)
}

func TestRequestShutdownCancelsTheActualWarmWorkerBeforeJoin(t *testing.T) {
	root := &Supervisor{}
	local, remote := net.Pipe()
	defer remote.Close()
	owned, ok := root.ownRequestConnectionV1(local)
	if !ok {
		t.Fatal("request was not admitted")
	}
	cancelled := make(chan struct{})
	scope := &nativeStopScopeV1{warmCancel: func() { close(cancelled) }}
	client := &client{conn: local, nativeStop: scope}
	root.bindRequestClientV1(owned, client)
	root.cancelRequestConnectionsV1()
	select {
	case <-cancelled:
	default:
		t.Fatal("shutdown did not cancel the actual registered warm worker")
	}
	root.finishRequestConnectionV1(owned)
	root.joinRequestConnectionsV1()
}
