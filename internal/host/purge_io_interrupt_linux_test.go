//go:build linux

package host

import (
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type purgeInterruptProbeV4 struct {
	net.Conn
	entered, release chan struct{}
	calls            atomic.Int32
	failure          error
}

func (connection *purgeInterruptProbeV4) SetDeadline(time.Time) error {
	connection.calls.Add(1)
	close(connection.entered)
	<-connection.release
	return connection.failure
}

func TestPurgeInterruptGetterCannotConsumeFirstActualActionV4(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	failure := errors.New("original deadline failed")
	connection := &purgeInterruptProbeV4{Conn: left, entered: make(chan struct{}), release: make(chan struct{}), failure: failure}
	t.Cleanup(func() {
		select {
		case <-connection.release:
		default:
			close(connection.release)
		}
	})
	line := &StartupLineV1{conn: connection}
	owner := &purgeNativeRetirementsV4{}
	interruptDone := make(chan struct{})
	go func() { defer close(interruptDone); owner.interruptLineV4(line) }()
	<-connection.entered
	diagnostics := make(chan error, 1)
	go func() { diagnostics <- owner.diagnosticsV4() }()
	select {
	case err := <-diagnostics:
		t.Fatalf("getter returned before the real interrupt: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(connection.release)
	<-interruptDone
	select {
	case err := <-diagnostics:
		if !errors.Is(err, failure) {
			t.Fatalf("lost first interrupt diagnostic: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("actual interrupt did not join")
	}
	owner.interruptLineV4(line)
	if connection.calls.Load() != 1 {
		t.Fatal("retry replaced the first native interrupt outcome")
	}
	if err := owner.checkStartupLineV4(line); err == nil {
		t.Fatal("diagnostic I/O became native line-retirement proof")
	}
}
