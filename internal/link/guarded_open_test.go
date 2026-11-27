package link

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func editorGuardPairV1(t *testing.T, wrap func(net.Conn) net.Conn) (*Session, *Session) {
	t.Helper()
	left, right := net.Pipe()
	var local net.Conn = left
	if wrap != nil {
		local = wrap(left)
	}
	client := ContinueEditorSSH(local, bufio.NewReader(local), Guest)
	server := ContinueEditorSSH(right, bufio.NewReader(right), Supervisor)
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	return client, server
}

func editorGuardAcceptV1(t *testing.T, peer *Session) *Stream {
	t.Helper()
	ctx, end := context.WithTimeout(t.Context(), 3*time.Second)
	defer end()
	stream, err := peer.Accept(ctx)
	if err != nil {
		t.Fatal("peer could not accept the next contiguous stream", err)
	}
	t.Cleanup(func() { _ = stream.SetDeadline(time.Now()); _ = stream.Close() })
	return stream
}

func TestGuardRefusalKeepsExistingDataAndNextPeerID(t *testing.T) {
	client, server := editorGuardPairV1(t, nil)
	first, err := client.Open(t.Context(), KindExec, nil)
	if err != nil {
		t.Fatal(err)
	}
	peer := editorGuardAcceptV1(t, server)
	refused := errors.New("original source changed")
	if stream, err := client.OpenGuarded(t.Context(), KindExec, nil, func(context.Context) (func(), error) { return nil, refused }); stream != nil || !errors.Is(err, refused) {
		t.Fatal("source refusal was not delivered", stream, err)
	}
	written := make(chan error, 1)
	go func() { _, err := first.Write([]byte("unchanged\x00\x1b[31m")); written <- err }()
	data := make([]byte, len("unchanged\x00\x1b[31m"))
	if _, err = io.ReadFull(peer, data); err != nil || string(data) != "unchanged\x00\x1b[31m" {
		t.Fatal("guard refusal damaged an existing byte stream", string(data), err)
	}
	if err = <-written; err != nil {
		t.Fatal(err)
	}
	second, err := client.OpenGuarded(t.Context(), KindExec, nil, func(context.Context) (func(), error) { return func() {}, nil })
	if err != nil {
		t.Fatal(err)
	}
	_ = editorGuardAcceptV1(t, server)
	_ = second.SetDeadline(time.Now())
	_ = second.Close()
	_ = first.SetDeadline(time.Now())
	_ = first.Close()
	if err = server.Err(); err != nil {
		t.Fatal("a refused guard consumed a contiguous wire ID", err)
	}
}

func TestQueuedGuardCancellationConsumesNoPeerID(t *testing.T) {
	client, server := editorGuardPairV1(t, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, err := client.OpenGuarded(t.Context(), KindExec, nil, func(ctx context.Context) (func(), error) {
			close(entered)
			select {
			case <-release:
				return func() {}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
		firstDone <- err
	}()
	<-entered
	queued, cancel := context.WithCancel(t.Context())
	secondDone := make(chan error, 1)
	go func() {
		_, err := client.OpenGuarded(queued, KindExec, nil, func(context.Context) (func(), error) {
			t.Error("a cancelled queued source guard ran")
			return func() {}, nil
		})
		secondDone <- err
	}()
	// Queue occupancy is a synchronisation observation, never permission.
	deadline := time.Now().Add(3 * time.Second)
	for {
		client.mu.Lock()
		waiting := len(client.guardedOpens) != 0
		client.mu.Unlock()
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second open did not reach the stopped writer")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-secondDone; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	_ = editorGuardAcceptV1(t, server)
	if _, err := client.OpenGuarded(t.Context(), KindExec, nil, func(context.Context) (func(), error) { return func() {}, nil }); err != nil {
		t.Fatal(err)
	}
	_ = editorGuardAcceptV1(t, server)
	if err := server.Err(); err != nil {
		t.Fatal("queued cancellation left a wire ID gap", err)
	}
}

type editorBlockedWriterV1 struct {
	net.Conn
	entered chan struct{}
	closed  chan struct{}
	once    sync.Once
	joined  atomic.Bool
}

func (c *editorBlockedWriterV1) Write(data []byte) (int, error) {
	c.once.Do(func() { close(c.entered) })
	<-c.closed
	c.joined.Store(true)
	return 0, net.ErrClosed
}
func (c *editorBlockedWriterV1) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return c.Conn.Close()
}

func TestGuardRetainsSourceUntilBlockedRawWriteJoins(t *testing.T) {
	var blocked *editorBlockedWriterV1
	client, _ := editorGuardPairV1(t, func(conn net.Conn) net.Conn {
		blocked = &editorBlockedWriterV1{Conn: conn, entered: make(chan struct{}), closed: make(chan struct{})}
		return blocked
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var held atomic.Bool
	done := make(chan error, 1)
	go func() {
		_, err := client.OpenGuarded(ctx, KindExec, nil, func(context.Context) (func(), error) {
			held.Store(true)
			return func() {
				if !blocked.joined.Load() {
					t.Error("source released before the actual raw write returned")
				}
				held.Store(false)
			}, nil
		})
		done <- err
	}()
	<-blocked.entered
	if !held.Load() {
		t.Fatal("source was not held at the physical raw write")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("admitted cancellation did not interrupt and join the actual raw writer")
	}
	if held.Load() {
		t.Fatal("source release was lost after actual transport close and writer join")
	}
}

func TestGuardResultCannotWinAnAlreadyCancelledCaller(t *testing.T) {
	client, server := editorGuardPairV1(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := client.OpenGuarded(ctx, KindExec, nil, func(context.Context) (func(), error) { return cancel, nil })
	if stream != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("a completed raw write returned a live stream after cancellation", stream, err)
	}
	_ = editorGuardAcceptV1(t, server)
	if _, err = client.OpenGuarded(t.Context(), KindExec, nil, func(context.Context) (func(), error) { return func() {}, nil }); err != nil {
		t.Fatal(err)
	}
	_ = editorGuardAcceptV1(t, server)
}
