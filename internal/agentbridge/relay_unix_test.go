//go:build unix

package agentbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/link"
)

func agentUnixPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	conns := make([]net.Conn, 0, 2)
	files := []*os.File{os.NewFile(uintptr(fds[0]), "agent-relay-test"), os.NewFile(uintptr(fds[1]), "agent-relay-test")}
	defer func() {
		for _, file := range files {
			_ = file.Close()
		}
	}()
	t.Cleanup(func() {
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
	for _, f := range files {
		conn, err := net.FileConn(f)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
		if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	return conns[0], conns[1]
}

type agentRelayPair struct {
	left, relayLeft, relayRight, right net.Conn
}

func realAgentRelayPair(t *testing.T, viaLink bool) agentRelayPair {
	t.Helper()
	relayRight, right := agentUnixPair(t)
	if !viaLink {
		left, relayLeft := agentUnixPair(t)
		return agentRelayPair{left, relayLeft, relayRight, right}
	}
	a, b := net.Pipe()
	supervisor := link.New(a, link.Supervisor, Options())
	guest := link.New(b, link.Guest, Options())
	t.Cleanup(func() { _ = supervisor.Close(); _ = guest.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	left, err := guest.Open(ctx, link.KindAgent, validScope())
	if err != nil {
		t.Fatal(err)
	}
	relayLeft, err := supervisor.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, stream := range []*link.Stream{left, relayLeft} {
		if err := stream.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	return agentRelayPair{left, relayLeft, relayRight, right}
}

func startAgentRelay(t *testing.T, ctx context.Context, a, b net.Conn) <-chan error {
	t.Helper()
	result, done := make(chan error, 1), make(chan struct{})
	go func() { defer close(done); result <- Relay(ctx, a, b) }()
	t.Cleanup(func() {
		_ = a.SetDeadline(time.Now())
		_ = b.SetDeadline(time.Now())
		_ = a.Close()
		_ = b.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("agent relay did not stop after closing its owned endpoints")
		}
	})
	return result
}

func halfCloseAgent(conn net.Conn) error {
	if half, ok := conn.(interface{ CloseWrite() error }); ok {
		return half.CloseWrite()
	}
	return errors.New("test endpoint cannot half-close")
}

func TestRelayUnixAndLinkHalfClosePreserveTheFinalResponse(t *testing.T) {
	for _, viaLink := range []bool{false, true} {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("link=%v/reverse=%v", viaLink, reverse), func(t *testing.T) {
				pair := realAgentRelayPair(t, viaLink)
				client, server := pair.left, pair.right
				if reverse {
					client, server = server, client
				}
				a, b := &relayProbe{Conn: pair.relayLeft}, &relayProbe{Conn: pair.relayRight}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				relay := startAgentRelay(t, ctx, a, b)
				request := bytes.Repeat([]byte{0, 1, 2, 255}, 20000)
				response := bytes.Repeat([]byte("final agent response\x00"), 4000)
				serverDone := make(chan error, 1)
				serverFinished := make(chan struct{})
				t.Cleanup(func() {
					_ = client.SetDeadline(time.Now())
					_ = server.SetDeadline(time.Now())
					_ = client.Close()
					_ = server.Close()
					select {
					case <-serverFinished:
					case <-time.After(5 * time.Second):
						t.Error("test agent did not stop after its owned endpoints closed")
					}
				})
				go func() {
					defer close(serverFinished)
					got, err := io.ReadAll(io.LimitReader(server, int64(len(request)+1)))
					if err != nil {
						serverDone <- fmt.Errorf("read the relayed request: %w", err)
						return
					}
					if !bytes.Equal(got, request) {
						serverDone <- errors.New("the relay changed request bytes")
						return
					}
					if err := writeBytes(server, response); err != nil {
						serverDone <- err
						return
					}
					serverDone <- halfCloseAgent(server)
				}()
				if err := writeBytes(client, request); err != nil {
					t.Fatal(err)
				}
				if err := halfCloseAgent(client); err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(io.LimitReader(client, int64(len(response)+1)))
				if err != nil || !bytes.Equal(got, response) {
					t.Fatalf("request EOF lost the final response: bytes=%d err=%v", len(got), err)
				}
				if err := <-serverDone; err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-relay:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("relay did not finish both half-closes")
				}
				a.checkBuffers(t)
				b.checkBuffers(t)
			})
		}
	}
}

// This wrapper stalls a real endpoint's Write without depending on kernel
// socket-buffer sizes. Cancellation must reach the controlled Write through
// an expired deadline, which is the Relay cancellation contract.
type blockedAgentWrite struct {
	net.Conn
	entered chan struct{}
	abort   chan struct{}
	start   sync.Once
	stop    sync.Once
}

func (c *blockedAgentWrite) Write([]byte) (int, error) {
	c.start.Do(func() { close(c.entered) })
	<-c.abort
	return 0, os.ErrDeadlineExceeded
}

func (c *blockedAgentWrite) SetDeadline(t time.Time) error {
	if !t.IsZero() && !time.Now().Before(t) {
		c.stop.Do(func() { close(c.abort) })
	}
	return c.Conn.SetDeadline(t)
}

func (c *blockedAgentWrite) Close() error {
	c.stop.Do(func() { close(c.abort) })
	return c.Conn.Close()
}

func (c *blockedAgentWrite) CloseWrite() error { return halfCloseAgent(c.Conn) }

func TestRelayCancellationInterruptsBlockedWritesInBothDirections(t *testing.T) {
	for _, viaLink := range []bool{false, true} {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("link=%v/reverse=%v", viaLink, reverse), func(t *testing.T) {
				pair := realAgentRelayPair(t, viaLink)
				a, b := pair.relayLeft, pair.relayRight
				source, destination := pair.left, b
				if reverse {
					source, destination = pair.right, a
				}
				blocked := &blockedAgentWrite{Conn: destination, entered: make(chan struct{}), abort: make(chan struct{})}
				if reverse {
					a = blocked
				} else {
					b = blocked
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				relay := startAgentRelay(t, ctx, a, b)
				if err := writeBytes(source, []byte("pending agent message")); err != nil {
					t.Fatal(err)
				}
				select {
				case <-blocked.entered:
				case <-time.After(5 * time.Second):
					t.Fatal("relay did not reach the controlled blocked writer")
				}
				cancel()
				select {
				case err := <-relay:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("relay cancellation = %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("cancelled relay retained a blocked read or write")
				}
			})
		}
	}
}
