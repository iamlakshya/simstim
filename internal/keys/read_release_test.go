package keys

import (
	"context"
	"errors"
	"testing"
	"time"
)

type claimedCancelReader struct{ *stuckReader }

func (r claimedCancelReader) Cancel() bool {
	r.stuckReader.Cancel()
	return true
}

// A successful cancellation request does not prove that the console read
// ended. Its release deadline must let the caller restore the terminal.
func TestReadUntilBoundsAReadWhoseCancelClaimsSuccess(t *testing.T) {
	r := claimedCancelReader{&stuckReader{
		reading: make(chan struct{}), cancels: make(chan struct{}, 1), release: make(chan struct{}),
	}}
	waiting := make(chan time.Duration, 1)
	expired := make(chan time.Time, 1)
	oldAfter := afterReadReleaseWait
	afterReadReleaseWait = func(d time.Duration) <-chan time.Time {
		waiting <- d
		return expired
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		done <- ReadUntil(ctx, r, func([]byte) bool { return true })
	}()
	defer func() {
		cancel()
		close(expired)
		close(r.release)
		<-ended
		afterReadReleaseWait = oldAfter
	}()
	select {
	case <-r.reading:
	case <-time.After(5 * time.Second):
		t.Fatal("the read never started")
	}
	cancel()
	select {
	case wait := <-waiting:
		if wait != 5*time.Second {
			t.Errorf("release deadline = %v, want five seconds", wait)
		}
	case <-time.After(time.Second):
		t.Fatal("successful Cancel did not start a bounded release wait")
	}
	select {
	case err := <-done:
		t.Fatalf("ReadUntil ended before its release deadline: %v", err)
	default:
	}
	expired <- time.Now()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("ReadUntil = %v, want the original cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ReadUntil still waits after the release deadline")
	}
}
