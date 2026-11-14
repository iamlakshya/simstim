package agentbridge

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConnRetainsBufferedIdentityTailAndEveryByte(t *testing.T) {
	tail := []byte{0, 1, 2, 255, '\x1b', '[', 'm'}
	input := bufio.NewReaderSize(bytes.NewReader(append([]byte("identity\n"), tail...)), 4096)
	if _, err := input.ReadString('\n'); err != nil || input.Buffered() != len(tail) {
		t.Fatalf("test did not retain the next protocol's buffered bytes: %d, %v", input.Buffered(), err)
	}
	var output bytes.Buffer
	c := NewConn(input, &output, nil)
	var _ net.Conn = c
	got, err := io.ReadAll(c)
	if err != nil || !bytes.Equal(got, tail) {
		t.Fatalf("buffered tail=%x, %v", got, err)
	}
	if n, err := c.Write(tail); n != len(tail) || err != nil || !bytes.Equal(output.Bytes(), tail) {
		t.Fatalf("outbound bytes=%x n=%d err=%v", output.Bytes(), n, err)
	}
	if _, err := c.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("EOF was wrapped or lost: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConnPreservesTheUnderlyingShortWriteContract(t *testing.T) {
	failed := errors.New("controlled pipe write failure")
	for _, result := range []struct {
		n   int
		err error
	}{{2, nil}, {2, failed}, {0, failed}, {100, nil}} {
		calls := 0
		c := NewConn(bytes.NewReader(nil), writerFunc(func([]byte) (int, error) {
			calls++
			return result.n, result.err
		}), nil)
		n, err := c.Write([]byte("data"))
		if n != result.n || err != result.err || calls != 1 {
			t.Fatalf("adapter changed a writer result: n=%d err=%v calls=%d", n, err, calls)
		}
	}
}

func TestConnReportsUnavailableDeadlinesWithoutChangingOwnership(t *testing.T) {
	closes := 0
	c := NewConn(bytes.NewReader(nil), io.Discard, func() error { closes++; return nil })
	for name, set := range map[string]func(time.Time) error{
		"both": c.SetDeadline, "read": c.SetReadDeadline, "write": c.SetWriteDeadline,
	} {
		for _, when := range []time.Time{{}, time.Now().Add(-time.Second), time.Now().Add(time.Hour)} {
			if err := set(when); !errors.Is(err, os.ErrNoDeadline) {
				t.Fatalf("%s deadline pretended to be supported: %v", name, err)
			}
		}
	}
	if closes != 0 || c.LocalAddr().Network() != "agent-stdio" || c.RemoteAddr().Network() != "agent-stdio" || c.LocalAddr().String() != "local" || c.RemoteAddr().String() != "remote" {
		t.Fatal("deadline/address inspection changed transport ownership")
	}
}

func TestConnCloseInterruptsBothDirectionsOnceWithoutJoiningTheReader(t *testing.T) {
	in, input := io.Pipe()
	output, out := io.Pipe()
	t.Cleanup(func() { in.Close(); input.Close(); output.Close(); out.Close() })
	failed := errors.New("controlled transport close")
	var calls atomic.Int32
	c := NewConn(in, out, func() error {
		calls.Add(1)
		_ = in.Close()
		_ = out.Close()
		return failed
	})
	readDone, writeDone := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := c.Read(make([]byte, 1))
		// This is the link reader's failure path. Close must not join it.
		if closeErr := c.Close(); closeErr != failed {
			err = closeErr
		}
		readDone <- err
	}()
	go func() { _, err := c.Write([]byte("pending")); writeDone <- err }()
	results := make(chan error, 16)
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() { defer group.Done(); results <- c.Close() }()
	}
	joined := make(chan struct{})
	go func() { group.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(5 * time.Second):
		t.Fatal("transport Close waited on its own reader")
	}
	for range 16 {
		if err := <-results; err != failed {
			t.Fatalf("concurrent Close lost its one result: %v", err)
		}
	}
	for name, done := range map[string]<-chan error{"read": readDone, "write": writeDone} {
		select {
		case err := <-done:
			if err == nil {
				t.Fatalf("pending %s succeeded after owned transport close", name)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("owned transport close did not interrupt %s", name)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("transport callback ran %d times", calls.Load())
	}
}
