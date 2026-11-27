package egress

import (
	"net"
	"sync"
	"testing"
	"time"
)

type observedJudgeConn struct {
	net.Conn
	entered chan struct{}
	once    sync.Once
}

func (conn *observedJudgeConn) Read(buffer []byte) (int, error) {
	conn.once.Do(func() { close(conn.entered) })
	return conn.Conn.Read(buffer)
}

func TestJudgeOriginCloseClosesSocketAndJoinsItsAdmittedRead(t *testing.T) {
	local, peer := net.Pipe()
	defer peer.Close()
	origin := newJudgeOrigin()
	entered := make(chan struct{})
	owned, err := origin.attach(&observedJudgeConn{Conn: local, entered: entered})
	if err != nil {
		t.Fatal(err)
	}
	readDone := make(chan struct{})
	go func() { defer close(readDone); _, _ = owned.Read(make([]byte, 1)) }()
	<-entered
	origin.close()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("physical origin read was not joined")
	}
	if _, err := owned.Write([]byte("after close")); err == nil {
		t.Fatal("closed judge origin admitted a write")
	}
	if _, err := owned.Read(make([]byte, 1)); err == nil {
		t.Fatal("closed judge origin admitted a read")
	}
}

func TestJudgeOriginCloseWaitsForAndRejectsLateDialCompletion(t *testing.T) {
	origin := newJudgeOrigin()
	if !origin.beginDial() {
		t.Fatal("initial origin dial refused")
	}
	closed := make(chan struct{})
	go func() { origin.close(); close(closed) }()
	deadline := time.Now().Add(time.Second)
	for {
		origin.mu.Lock()
		closing := origin.closing
		origin.mu.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("origin closure did not begin")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-closed:
		t.Fatal("origin closed before its admitted dial finished")
	default:
	}
	local, peer := net.Pipe()
	defer peer.Close()
	if owned, err := origin.attach(local); err == nil || owned != nil {
		t.Fatal("late dial escaped the closing source owner")
	}
	origin.dials.Done()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("late closed dial was not joined")
	}
	if origin.beginDial() {
		t.Fatal("closed origin admitted another dial")
	}
	if _, err := peer.Write([]byte("after close")); err == nil {
		t.Fatal("late dial socket remained open")
	}
}
