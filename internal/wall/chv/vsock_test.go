package chv

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func vsockPair(t *testing.T, reply []byte) (net.Conn, <-chan error) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	done := make(chan error, 1)
	go func() {
		request := make([]byte, len("CONNECT 1024\n"))
		if _, err := io.ReadFull(b, request); err != nil {
			done <- err
			return
		}
		if string(request) != "CONNECT 1024\n" {
			done <- errors.New("wrong connect command")
			return
		}
		_, err := b.Write(reply)
		done <- err
	}()
	return a, done
}

func TestVsockAcknowledgesAllocatedPortAndPreservesPrefetch(t *testing.T) {
	payload := []byte("\x01\x00\x1b[31mexact guest bytes")
	conn, done := vsockPair(t, append([]byte("OK 49152\n"), payload...))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reader, err := connectVsock(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("vsock prefetch changed guest bytes")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestVsockRejectsClosedMalformedAndOversizedReplies(t *testing.T) {
	for _, reply := range []string{"NO 1024\n", "OK 0\n", "OK 4294967296\n", "OK wrong\n", strings.Repeat("x", 256) + "\n"} {
		conn, _ := vsockPair(t, []byte(reply))
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := connectVsock(ctx, conn)
		cancel()
		conn.Close()
		if err == nil {
			t.Fatalf("bad VMM reply accepted: %q", reply)
		}
	}
}

func TestVsockRetriesWithoutCreatingAnyListener(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	attempts := 0
	pauses := 0
	conn, err := connectGuest(ctx, "/private/vsock", func(context.Context, string) (net.Conn, error) {
		attempts++
		if attempts < 3 {
			return nil, errors.New("guest not listening")
		}
		conn, _ := vsockPair(t, []byte("OK 65535\n"))
		return conn, nil
	}, func(context.Context) error { pauses++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if attempts != 3 || pauses != 2 {
		t.Fatalf("attempts%d pauses%d", attempts, pauses)
	}
}

func TestVsockCancellationInterruptsWaitingReply(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := connectVsock(ctx, a); done <- err }()
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancelled vsock handshake succeeded")
	}
}
