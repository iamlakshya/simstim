package link

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestDuplexCancelJoinsBothBlockedDirections(t *testing.T) {
	left, source := net.Pipe()
	right, destination := net.Pipe()
	defer source.Close()
	defer destination.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RelayDuplex(ctx, left, right) }()
	payload := []byte{0, '\x1b', '[', '3', '1', 'm', '\n', 255}
	read := make(chan error, 1)
	go func() {
		got := make([]byte, len(payload))
		_, err := io.ReadFull(destination, got)
		if err == nil {
			for i := range got {
				if got[i] != payload[i] {
					t.Errorf("opaque byte %d changed", i)
				}
			}
		}
		read <- err
	}()
	if _, err := source.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := <-read; err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("duplex cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("duplex cancellation left a blocked reader")
	}
}
