package keys

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// stuckReader's read never returns, and it cannot be cancelled, as a
// Windows console's read can be. reading closes when its read starts.
type stuckReader struct {
	reading chan struct{}
	cancels chan struct{}
	release chan struct{}
}

func (s *stuckReader) Read([]byte) (int, error) {
	close(s.reading)
	<-s.release
	return 0, io.EOF
}

func (s *stuckReader) Cancel() bool {
	s.cancels <- struct{}{}
	return false
}

// A read that cannot be cancelled does not keep ReadUntil: it returns at
// the end of its context all the same.
func TestReadUntilEndsWhenTheReadCannotBeCancelled(t *testing.T) {
	s := &stuckReader{reading: make(chan struct{}), cancels: make(chan struct{}, 1), release: make(chan struct{})}
	defer close(s.release)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ReadUntil(ctx, s, func([]byte) bool { return true }) }()
	<-s.reading
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("ReadUntil = %v, want the context's end", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReadUntil waits for a read that cannot be cancelled")
	}
	select {
	case <-s.cancels:
	default:
		t.Error("ReadUntil did not ask the read to cancel")
	}
}

type cancelled struct{ io.Reader }

func (cancelled) Cancel() bool { return true }

// slowCancel's read waits until a cancel, which works: ReadUntil waits for
// that read to end before it returns, so that its caller may close the
// reader.
type slowCancel struct {
	reading, cancel chan struct{}
	ended           bool
}

func (s *slowCancel) Read([]byte) (int, error) {
	close(s.reading)
	<-s.cancel
	s.ended = true
	return 0, errors.New("read cancelled")
}

func (s *slowCancel) Cancel() bool {
	close(s.cancel)
	return true
}

func TestReadUntilWaitsForACancelledRead(t *testing.T) {
	s := &slowCancel{reading: make(chan struct{}), cancel: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-s.reading
		cancel()
	}()
	if err := ReadUntil(ctx, s, func([]byte) bool { return true }); !errors.Is(err, context.Canceled) || !s.ended {
		t.Errorf("ReadUntil = %v, and the read ended: %v", err, s.ended)
	}
}

// ReadUntil passes every chunk on, and ends when fn says so or the input
// ends.
func TestReadUntilPassesTheChunks(t *testing.T) {
	var got strings.Builder
	err := ReadUntil(context.Background(), cancelled{strings.NewReader("abc")}, func(b []byte) bool {
		got.Write(b)
		return true
	})
	if err != nil || got.String() != "abc" {
		t.Errorf("ReadUntil = %v, read %q", err, got.String())
	}
	calls := 0
	err = ReadUntil(context.Background(), cancelled{strings.NewReader("abc")}, func([]byte) bool {
		calls++
		return false
	})
	if err != nil || calls != 1 {
		t.Errorf("ReadUntil after fn says stop = %v, %d calls", err, calls)
	}
}
